package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/softdelete"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ErrNotDeleted is returned by Restore when the target row is not soft-deleted.
// The row was never deleted, or it has already been restored, or it was purged
// from the database. The CLI collapses this with ErrNotFound.
var ErrNotDeleted = errors.New("event: row not soft-deleted (may have been purged)")

// UndoKind discriminates the three reversible delete shapes.
type UndoKind int

const (
	// UndoKindSingle is a standalone or single-override soft-delete. Restore
	// clears deleted_at on exactly one row.
	UndoKindSingle UndoKind = iota
	// UndoKindSeries is a full series soft-delete (DeleteSeries). Restore
	// clears deleted_at on every row with the UID.
	UndoKindSeries
	// UndoKindFromInstance is a truncation (DeleteFromInstance). Restore
	// rewrites the master's RRULE back to the pre-truncation value AND
	// clears deleted_at on all overrides that were soft-deleted by the
	// truncation.
	UndoKindFromInstance
)

// UndoMeta carries the data a TUI or CLI Restore caller needs to reverse a
// previously-soft-deleted operation. It is intentionally small (no blobs, no
// transient children). The per-Kind Restore method does the hard work. That
// method finds the actual rows by UID.
type UndoMeta struct {
	Kind      UndoKind
	UID       string
	Label     string
	DeletedAt time.Time

	// CalendarID is the calendar of the series, or of the deleted row.
	// Two calendars can hold the same UID (issue #842), so Restore finds
	// the master in this calendar. 0 names no calendar: one master row of
	// the UID is then the only choice.
	CalendarID int64

	// UndoKindSingle only. DeleteWithUndo or DeleteInstanceWithUndo sets
	// this field when the delete targets an override instance. A standalone
	// master delete leaves this field empty.
	RecurrenceID string

	// UndoKindFromInstance only. CutoffTime is the truncation cutoff. It finds
	// the event_truncate_deletes log row that Undo reverses. Then the RRULE,
	// trimmed RDATEs, and the exact overrides the truncation hid are all
	// restored. MasterUpdatedBefore is the stale-master guard baseline.
	CutoffTime          time.Time
	MasterUpdatedBefore time.Time
}

// DeleteWithUndo soft-deletes an event by ID and returns the metadata needed
// to reverse it. For an override, EXDATE mutation on the master is part of
// the Delete flow. The returned UndoMeta carries the override's
// recurrence_id. Undo then un-hides exactly that instance. Without it, undo
// falls back to a UID-wide restore that would also resurrect other
// soft-deleted overrides of the same series.
func (s *Service) DeleteWithUndo(ctx context.Context, id int64) (UndoMeta, error) {
	r, err := s.q.GetEvent(ctx, id)
	if err != nil {
		return UndoMeta{}, err
	}
	evt := FromStorage(r)
	if err := s.Delete(ctx, id); err != nil {
		return UndoMeta{}, err
	}
	return UndoMeta{
		Kind:         UndoKindSingle,
		UID:          evt.UID,
		Label:        evt.Title,
		DeletedAt:    time.Now().UTC(),
		CalendarID:   evt.CalendarID,
		RecurrenceID: evt.RecurrenceID,
	}, nil
}

// DeleteInstanceWithUndo excludes an occurrence and returns undo metadata.
// The overridden instance (if any) is soft-deleted; on Restore we un-hide it
// and remove the EXDATE we added. calendarID names the calendar of the
// series (see findSeriesMasterTx).
func (s *Service) DeleteInstanceWithUndo(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (UndoMeta, error) {
	master, _, err := findSeriesMasterTx(ctx, s.q, calendarID, uid)
	if err != nil {
		return UndoMeta{}, wrapMasterLookup(err)
	}
	label := master.Title
	if err := s.DeleteInstance(ctx, master.CalendarID, uid, instanceTime); err != nil {
		return UndoMeta{}, err
	}
	return UndoMeta{
		Kind:         UndoKindSingle,
		UID:          uid,
		Label:        label,
		DeletedAt:    time.Now().UTC(),
		CalendarID:   master.CalendarID,
		RecurrenceID: instanceTime.UTC().Format(time.RFC3339),
	}, nil
}

// DeleteFromInstanceWithUndo truncates the series at instanceTime and returns
// the cutoff plus master UpdatedAt. Restore can then reverse the truncation
// exactly via the event_truncate_deletes log row. calendarID names the
// calendar of the series (see findSeriesMasterTx).
func (s *Service) DeleteFromInstanceWithUndo(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (UndoMeta, error) {
	master, _, err := findSeriesMasterTx(ctx, s.q, calendarID, uid)
	if err != nil {
		return UndoMeta{}, wrapMasterLookup(err)
	}

	// deleteFromInstance bumps the master's updated_at (UpdateEventRecurrenceRule
	// sets updated_at=now) and returns that post-truncation value, read back
	// inside the truncation transaction. The stale-master guard in
	// restoreFromInstance must expect this value, not the pre-truncation one.
	// Otherwise the truncation's own write looks like a concurrent external edit
	// and Undo always fails.
	//
	// Capture it in-tx rather than via a separate read after commit. Then a
	// concurrent external edit that lands between commit and read is not
	// mistaken for the baseline. Only edits that advance updated_at *past*
	// this point trip the guard.
	postUpdated, err := s.deleteFromInstance(ctx, master.CalendarID, uid, instanceTime)
	if err != nil {
		return UndoMeta{}, err
	}

	return UndoMeta{
		Kind:                UndoKindFromInstance,
		UID:                 uid,
		Label:               master.Title,
		DeletedAt:           time.Now().UTC(),
		CalendarID:          master.CalendarID,
		CutoffTime:          instanceTime.UTC(),
		MasterUpdatedBefore: parseStorageTime(postUpdated),
	}, nil
}

// DeleteSeriesWithUndo soft-deletes a master + all overrides and returns undo
// metadata. Restore un-hides every row of the series in its calendar.
// calendarID names the calendar of the series (see findSeriesMasterTx).
func (s *Service) DeleteSeriesWithUndo(ctx context.Context, calendarID int64, uid string) (UndoMeta, error) {
	master, _, err := findSeriesMasterTx(ctx, s.q, calendarID, uid)
	if err != nil {
		return UndoMeta{}, wrapMasterLookup(err)
	}
	label := master.Title
	if err := s.DeleteSeries(ctx, master.CalendarID, uid); err != nil {
		return UndoMeta{}, err
	}
	return UndoMeta{
		Kind:       UndoKindSeries,
		UID:        uid,
		Label:      label,
		DeletedAt:  time.Now().UTC(),
		CalendarID: master.CalendarID,
	}, nil
}

// RestoreUndo reverses a soft-delete operation recorded in UndoMeta. It
// dispatches by Kind. For FromInstance kinds, it also rewrites the master's
// RRULE back to the pre-truncation value in the same transaction.
func (s *Service) RestoreUndo(ctx context.Context, meta UndoMeta) error {
	// The guard covers the calendars of the series only. A calendar
	// without a row of the series, for example a read-only copy of the
	// UID, does not block the restore (issue #842).
	var err error
	if meta.CalendarID != 0 {
		err = ensureSeriesWritableIn(ctx, s.q, meta.CalendarID, meta.UID)
	} else {
		err = s.ensureSeriesWritable(ctx, meta.UID)
	}
	if err != nil {
		return err
	}
	switch meta.Kind {
	case UndoKindSingle:
		return s.restoreSingle(ctx, meta.CalendarID, meta.UID, meta.RecurrenceID)
	case UndoKindSeries:
		return s.restoreSeries(ctx, meta.CalendarID, meta.UID)
	case UndoKindFromInstance:
		return s.restoreFromInstance(ctx, meta)
	default:
		return fmt.Errorf("unknown undo kind %d", meta.Kind)
	}
}

// RestoreByUID un-hides every soft-deleted row with the given UID (master and
// overrides). Used by the CLI `events restore <uid>` path. Returns
// ErrNotDeleted when the UID matches no soft-deleted rows (it is live, or
// unknown, or already purged). Callers can then report "not found" instead of
// a false success.
//
// Two calendars can hold the same UID (issue #842). The restore targets
// the series in the trash: one master row in the trash is the choice.
// With no master in the trash, the one live master names the series, and
// the restore un-hides its overrides. More masters of either kind give
// ErrAmbiguousUID: restore the series by the ID of its master instead.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	rows, err := s.q.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}
	var live, trashed []storage.Event
	for _, m := range rows {
		if m.DeletedAt == nil {
			live = append(live, m)
		} else {
			trashed = append(trashed, m)
		}
	}
	candidates := trashed
	if len(candidates) == 0 {
		candidates = live
	}
	if len(candidates) > 1 {
		return ErrAmbiguousUID
	}
	if len(candidates) == 0 {
		// No master row: the UID holds override rows only. Restore them
		// all, and reconcile every calendar they live on.
		if err := s.ensureSeriesWritable(ctx, uid); err != nil {
			return err
		}
		n, err := s.restoreEventsByUIDClearingExdates(ctx, uid)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotDeleted
		}
		return s.reconcileRestoredCalendars(ctx, uid)
	}
	master := candidates[0]
	if err := ensureSeriesWritableIn(ctx, s.q, master.CalendarID, uid); err != nil {
		return err
	}
	n, err := s.restoreSeriesRowsClearingExdates(ctx, master.CalendarID, uid)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotDeleted
	}
	return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
}

// reconcileRestoredCalendars reconciles the sync state of every calendar
// that holds a row of uid. The restore of a UID with no master row uses
// it: without this, a pending tombstone survives the restore, and the
// next sync would DELETE the series from the server right after a
// successful restore.
func (s *Service) reconcileRestoredCalendars(ctx context.Context, uid string) error {
	ids, err := s.distinctCalendarIDsByUID(ctx, uid)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.reconcileSyncAfterRestore(ctx, id, uid); err != nil {
			return err
		}
	}
	return nil
}

// RestoreByID un-hides a single soft-deleted row. Used by the CLI
// `events restore <id>` path. Reconciles sync state so subsequent pushes
// re-CREATE the resource on the server if necessary.
func (s *Service) RestoreByID(ctx context.Context, id int64) error {
	r, err := s.q.GetEventIncludingDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get event: %w", err)
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		return ErrNotDeleted
	}
	if err := calendaraccess.EnsureWritable(ctx, s.q, r.CalendarID, "VEVENT"); err != nil {
		return err
	}
	if r.RecurrenceID != "" {
		if err := s.restoreOverrideByID(ctx, r); err != nil {
			return err
		}
		return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
	}
	if err := s.q.RestoreEvent(ctx, id); err != nil {
		return fmt.Errorf("restore event: %w", err)
	}
	return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
}

// ListDeleted returns soft-deleted events for a calendar, newest first.
func (s *Service) ListDeleted(ctx context.Context, calendarID int64) ([]Event, error) {
	rows, err := s.q.ListDeletedEventsByCalendar(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

// GetIncludingDeleted returns a row by ID even if it has been soft-deleted.
// Used by the trash view's detail popup where the user wants to inspect
// what was deleted before restore.
func (s *Service) GetIncludingDeleted(ctx context.Context, id int64) (Event, error) {
	r, err := s.q.GetEventIncludingDeleted(ctx, id)
	if err != nil {
		return Event{}, err
	}
	return FromStorage(r), nil
}

// PurgeDeleted hard-deletes rows soft-deleted before olderThan. Returns the
// number of rows purged. Children cascade via the FK ON DELETE CASCADE.
func (s *Service) PurgeDeleted(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeSoftDeletedEvents(ctx, &cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeByID hard-deletes a single soft-deleted row. Returns ErrNotDeleted if
// the row is live (or absent). Callers then cannot purge a live event with
// the wrong ID.
func (s *Service) PurgeByID(ctx context.Context, id int64) error {
	n, err := s.q.PurgeEventByID(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotDeleted
	}
	return nil
}

// restoreSingle reverses one single-row delete. The undo paths use it for
// DeleteWithUndo and DeleteInstanceWithUndo. A non-empty recurrenceID
// restores exactly that instance: it un-hides the override row and clears
// the delete-recorded EXDATE for it. An empty recurrenceID targets the
// master row itself. calendarID is the calendar of the series, or of the
// deleted row (see findSeriesMasterIncludingDeletedTx).
func (s *Service) restoreSingle(ctx context.Context, calendarID int64, uid, recurrenceID string) error {
	r, _, err := findSeriesMasterIncludingDeletedTx(ctx, s.q, calendarID, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Might be an override-only delete where the master UID has no
			// master row. Fall back to a restore of the rows of the UID in
			// the calendar.
			_, err := s.restoreSeriesRowsClearingExdates(ctx, calendarID, uid)
			return err
		}
		return err
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		if recurrenceID != "" {
			if err := s.restoreEXDATEOnly(ctx, r.CalendarID, uid, recurrenceID); err != nil {
				return err
			}
			return nil
		}
		// Master is live; an override was probably the thing deleted.
		// Un-hide every deleted override of the series.
		_, err := s.restoreSeriesRowsClearingExdates(ctx, r.CalendarID, uid)
		return err
	}
	if err := s.q.RestoreEvent(ctx, r.ID); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
}

// restoreSeries reverses a series delete. calendarID is the calendar of
// the series (see findSeriesMasterIncludingDeletedTx).
func (s *Service) restoreSeries(ctx context.Context, calendarID int64, uid string) error {
	// Find the master (deleted rows included) for sync reconciliation context.
	master, _, err := findSeriesMasterIncludingDeletedTx(ctx, s.q, calendarID, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		calendarID = master.CalendarID
	}
	if _, err := s.restoreSeriesRowsClearingExdates(ctx, calendarID, uid); err != nil {
		return err
	}
	if err == nil {
		return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
	}
	// The master row is gone (it was purged on its own). Reconcile on every
	// calendar the restored rows live on, the same way RestoreByUID does.
	return s.reconcileRestoredCalendars(ctx, uid)
}

func (s *Service) restoreOverrideByID(ctx context.Context, r storage.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.RestoreEvent(ctx, r.ID); err != nil {
		return fmt.Errorf("restore event: %w", err)
	}
	// The master of the override is in the calendar of the row, or the
	// row is a legacy override of the one master of the UID. No master
	// leaves no EXDATE to clear. A legacy override of a UID with more
	// than one master belongs to no series (Delete recorded no EXDATE for
	// it), so there is nothing to clear either.
	master, _, err := findSeriesMasterIncludingDeletedTx(ctx, qtx, r.CalendarID, r.Uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrAmbiguousUID) {
		return fmt.Errorf("get master: %w", err)
	}
	if err == nil {
		if err := clearMasterEXDATE(ctx, qtx, master.CalendarID, r.Uid, r.RecurrenceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// restoreEXDATEOnly reverses an occurrence delete of the series in
// calendarID: it un-hides the override of the occurrence and clears the
// EXDATE that the delete recorded.
func (s *Service) restoreEXDATEOnly(ctx context.Context, calendarID int64, uid, recurrenceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// DeleteInstance soft-deletes the override at this recurrence_id (if one
	// existed) and adds the EXDATE. Un-hide it so undo restores the
	// customized occurrence, not just the base instance. No-ops when the
	// deleted instance had no override.
	if err := qtx.RestoreSeriesOverrideByRecurrenceID(ctx, storage.RestoreSeriesOverrideByRecurrenceIDParams{
		CalendarID:   calendarID,
		Uid:          uid,
		RecurrenceID: recurrenceID,
	}); err != nil {
		return fmt.Errorf("restore override: %w", err)
	}
	if err := clearMasterEXDATE(ctx, qtx, calendarID, uid, recurrenceID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, calendarID, uid)
}

// restoreSeriesRowsClearingExdates un-hides every soft-deleted row of the
// series of uid in calendarID (see SoftDeleteSeriesRowsInCalendar) and
// clears the master EXDATE of each override it un-hides, all in one
// transaction. It returns the number of rows it un-hides. calendarID 0
// names no calendar, so it un-hides every row of the UID.
func (s *Service) restoreSeriesRowsClearingExdates(ctx context.Context, calendarID int64, uid string) (int64, error) {
	if calendarID == 0 {
		return s.restoreEventsByUIDClearingExdates(ctx, uid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	recurrenceIDs, err := qtx.ListDeletedSeriesOverrideRecurrenceIDs(ctx, storage.ListDeletedSeriesOverrideRecurrenceIDsParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return 0, fmt.Errorf("list deleted override recurrence ids: %w", err)
	}
	n, err := qtx.RestoreSeriesRowsInCalendar(ctx, storage.RestoreSeriesRowsInCalendarParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return 0, fmt.Errorf("restore series rows: %w", err)
	}
	for _, recurrenceID := range recurrenceIDs {
		if err := clearMasterEXDATE(ctx, qtx, calendarID, uid, recurrenceID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// restoreEventsByUIDClearingExdates un-hides every soft-deleted row for uid and
// clears the master EXDATE for each override that was soft-deleted, all in one
// transaction. Returns the number of rows un-hidden so callers can distinguish
// a real restore from a no-op (live/unknown UID).
func (s *Service) restoreEventsByUIDClearingExdates(ctx context.Context, uid string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	recurrenceIDs, err := qtx.ListDeletedOverrideRecurrenceIDs(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("list deleted override recurrence ids: %w", err)
	}
	// The delete log is per calendar (issue #842). Each master calendar
	// of the UID clears the EXDATEs that its own log recorded.
	masters, err := qtx.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("list masters: %w", err)
	}
	n, err := qtx.RestoreEventsByUID(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("restore by uid: %w", err)
	}
	for _, m := range masters {
		for _, recurrenceID := range recurrenceIDs {
			if err := clearMasterEXDATE(ctx, qtx, m.CalendarID, uid, recurrenceID); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// clearMasterEXDATE reverses the EXDATE an instance-delete added for
// recurrenceID on the master event with uid in calendarID. The provenance
// contract lives in softdelete.ClearMasterEXDATE; this wrapper only binds
// the event queries to the active transaction so the override is never
// visible-but-excluded. The delete log and the master are per calendar:
// two calendars can hold the same UID (issue #842).
func clearMasterEXDATE(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recurrenceID string) error {
	return softdelete.ClearMasterEXDATE(ctx, softdelete.ExdateProvenance{
		GetDeleteLog: func(ctx context.Context) (int64, bool, error) {
			log, err := qtx.GetEventExdateDeleteByCalendarUIDRecurrence(ctx, storage.GetEventExdateDeleteByCalendarUIDRecurrenceParams{
				CalendarID: calendarID, Uid: uid, RecurrenceID: recurrenceID,
			})
			found, err := storage.Found(err)
			return log.ID, found, err
		},
		GetMaster: func(ctx context.Context) (int64, string, bool, error) {
			master, err := qtx.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
			found, err := storage.Found(err)
			return master.ID, storage.NullableToString(master.Exdates), found, err
		},
		UpdateExdates: func(ctx context.Context, masterID int64, exdates string) error {
			return qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
				Exdates: storage.StringToNullable(exdates),
				ID:      masterID,
			})
		},
		DeleteDeleteLog: qtx.DeleteEventExdateDelete,
	}, recurrenceID)
}

// restoreFromInstance reverses a "this and following" truncation for the TUI/CLI
// undo path. It applies the stale-master guard. Undo is rejected if an external
// edit advanced the master past the truncation. It then routes the reversal
// through restoreTruncationByLogID, the same provenance-aware path the trash
// view uses.
//
// That restores the RRULE, the trimmed RDATEs (issue #490), and only the
// overrides the truncation itself hid (issue #491, the #287 class on undo).
// The truncate-log row is consumed. No phantom "Series tail" trash entry
// then lingers for the now-live series.
func (s *Service) restoreFromInstance(ctx context.Context, meta UndoMeta) error {
	master, _, err := findSeriesMasterIncludingDeletedTx(ctx, s.q, meta.CalendarID, meta.UID)
	if err != nil {
		return wrapMasterLookup(err)
	}
	prevUpdated := parseStorageTime(master.UpdatedAt)
	if prevUpdated.After(meta.MasterUpdatedBefore.Add(time.Second)) {
		return fmt.Errorf("master advanced since delete (expected updated_at <= %s, got %s)",
			meta.MasterUpdatedBefore.Format(time.RFC3339), prevUpdated.Format(time.RFC3339))
	}

	log, err := s.q.GetEventTruncateDeleteByCalendarUIDAndCutoff(ctx, storage.GetEventTruncateDeleteByCalendarUIDAndCutoffParams{
		CalendarID: master.CalendarID,
		Uid:        meta.UID,
		CutoffTime: meta.CutoffTime.UTC().Format(time.RFC3339),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get truncate log: %w", err)
	}
	return s.restoreTruncationByLogID(ctx, log.ID)
}

// reconcileSyncAfterRestore mirrors the 3-case state machine from
// snapshot.go's Restore. For a freshly un-hidden row:
//   - Case A (local-only, no sync_resource): no-ops.
//   - Case B (tombstone present): clear the tombstone.
//   - Case C (tombstone + sync_resource both gone): MarkResourceDirty creates
//     a fresh sync_resource with remote_url=” so the next push allocates a
//     new href.
//
// Override rows don't own a sync_resource; callers pass the master's UID so
// the master is marked dirty instead, which covers the override on push.
func (s *Service) reconcileSyncAfterRestore(ctx context.Context, calendarID int64, uid string) error {
	if err := s.q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{
		CalendarID: calendarID,
		Uid:        uid,
	}); err != nil {
		return fmt.Errorf("clear tombstone after restore: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, calendarID, uid, "event"); err != nil {
		return fmt.Errorf("mark resource dirty after restore: %w", err)
	}
	return nil
}

func parseStorageTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeutil.StorageTimeFormat, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

// ErrHasOverrides is returned when a delete targets a recurring master
// event that has override instances. Use DeleteSeries instead.
var ErrHasOverrides = fmt.Errorf("event has overrides: use DeleteSeries to delete the entire series")

func (s *Service) Delete(ctx context.Context, id int64) error {
	r, err := s.q.GetEvent(ctx, id)
	if err != nil {
		return err
	}
	evt := FromStorage(r)

	if err := calendaraccess.EnsureWritable(ctx, s.q, evt.CalendarID, "VEVENT"); err != nil {
		return err
	}

	// If this is a recurring master, check for overrides. RDATE-only masters
	// (no RRULE) are recurring too, so guard on either rule or RDATEs (#415).
	if (evt.RecurrenceRule != "" || evt.RDates != "") && evt.RecurrenceID == "" {
		overrides, err := s.q.ListSeriesOverridesInCalendar(ctx, storage.ListSeriesOverridesInCalendarParams{
			CalendarID: evt.CalendarID, Uid: evt.UID,
		})
		if err != nil {
			return fmt.Errorf("check overrides: %w", err)
		}
		if len(overrides) > 0 {
			return ErrHasOverrides
		}
	}

	// If this is a standalone event (no recurrence or a solo master), create
	// a tombstone so the sync engine can send a DELETE to the server. The
	// tombstone and the soft-delete commit together: if the tombstone write
	// fails the soft-delete rolls back, so the next sync can never DELETE a
	// still-live event from the server (issue #107).
	if evt.RecurrenceID == "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, evt.CalendarID, evt.UID); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
		if err := qtx.SoftDeleteEvent(ctx, id); err != nil {
			return fmt.Errorf("soft-delete event: %w", err)
		}
		return tx.Commit()
	}

	// If this is an override, add EXDATE to the master so the instance
	// doesn't reappear on next expansion. The master's sync resource
	// becomes dirty (modified EXDATE), not the override.
	if evt.RecurrenceID != "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)

		// The master is in the calendar of the override, or the override
		// is a legacy override of the one live master of the UID. A
		// legacy override of a UID with more than one live master belongs
		// to no series: the expansion keys the overrides by calendar, so
		// no master shows it. The row goes without EXDATE bookkeeping,
		// like a row with no master.
		master, _, err := findSeriesMasterTx(ctx, qtx, evt.CalendarID, evt.UID)
		// A genuine lookup error (e.g. SQLITE_BUSY) must not collapse into the
		// "no master" path: that would soft-delete the override while skipping
		// its EXDATE/provenance bookkeeping, resurrecting the occurrence via
		// series expansion (issue #412). Only a missing master (ErrNoRows)
		// legitimately skips the bookkeeping.
		if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrAmbiguousUID) {
			return fmt.Errorf("get master: %w", err)
		}
		if err == nil {
			existing := ParseTimeList(storage.NullableToString(master.Exdates))
			recIDTime, parseErr := timeutil.ParseRecurrenceID(evt.RecurrenceID)
			if parseErr != nil {
				// A malformed recurrence_id can't be excluded from the
				// master, so soft-deleting the override would resurrect the
				// occurrence via series expansion. Fail loudly instead — the
				// restore path treats the same parse failure as fatal.
				return fmt.Errorf("parse recurrence_id %q: %w", evt.RecurrenceID, parseErr)
			}
			// All-day masters store recurrence_ids as full RFC 3339, so
			// ParseRecurrenceID yields a UTC-located time. Re-tag it as
			// date-only so the EXDATE serializes as VALUE=DATE matching
			// DTSTART;VALUE=DATE on export (RFC 5545 §3.8.5.1, issue #221).
			if master.AllDay == 1 {
				recIDTime = timeutil.AsDateOnly(recIDTime)
			}
			existing = append(existing, recIDTime)
			if err := qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
				Exdates: storage.StringToNullable(SerializeTimeList(existing)),
				ID:      master.ID,
			}); err != nil {
				return fmt.Errorf("update exdates: %w", err)
			}
			// Record provenance so restore knows this EXDATE was
			// delete-added (and may be stripped) rather than imported.
			if err := qtx.RecordEventExdateDelete(ctx, storage.RecordEventExdateDeleteParams{
				CalendarID:   master.CalendarID,
				Uid:          evt.UID,
				RecurrenceID: evt.RecurrenceID,
			}); err != nil {
				return fmt.Errorf("record exdate delete: %w", err)
			}
		}

		if err := qtx.SoftDeleteEvent(ctx, id); err != nil {
			return fmt.Errorf("soft-delete event: %w", err)
		}
		// Mark the master dirty — its EXDATE was modified — inside the same
		// transaction so a failed mark rolls the EXDATE change back rather than
		// committing a change that is never pushed (issue #107).
		if err := storage.MarkResourceDirty(ctx, tx, evt.CalendarID, evt.UID, "event"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	// Unreachable: RecurrenceID is either "" (handled above) or non-empty.
	return s.q.SoftDeleteEvent(ctx, id)
}

// DeleteInstance excludes a single occurrence of a recurring event by adding
// an EXDATE to the master. instanceTime is the StartTime of the occurrence.
// calendarID names the calendar of the series. Two calendars can hold the
// same UID (issue #842). With 0, one live master is the only choice, and
// more give ErrAmbiguousUID. See findSeriesMasterTx.
func (s *Service) DeleteInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Read the master inside the transaction so the EXDATE list we recompute
	// reflects a concurrent writer's changes (issue #116). Reading outside the
	// tx let a second instance-delete clobber the first one's EXDATE.
	master, masters, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return wrapMasterLookup(err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return err
	}

	existing := ParseTimeList(storage.NullableToString(master.Exdates))
	exdate := instanceTime.UTC()
	// For an all-day master, tag the EXDATE as date-only so it serializes as
	// VALUE=DATE matching DTSTART;VALUE=DATE on export; otherwise a strict
	// CalDAV server ignores the mismatched DATE-TIME EXDATE and the deleted
	// occurrence reappears (RFC 5545 §3.8.5.1, issue #221).
	if master.AllDay == 1 {
		exdate = timeutil.AsDateOnly(exdate)
	}
	existing = append(existing, exdate)
	if err := qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
		Exdates: storage.StringToNullable(SerializeTimeList(existing)),
		ID:      master.ID,
	}); err != nil {
		return fmt.Errorf("update exdates: %w", err)
	}

	recID := instanceTime.UTC().Format(time.RFC3339)
	override, oErr := findInstanceOverrideTx(ctx, qtx, master, masters, recID)
	// Only ErrNoRows means "this occurrence has no override row". A genuine
	// lookup error must abort. Otherwise the EXDATE and its provenance commit
	// while the live override row stays. The deleted occurrence then stays
	// visible through the override. This mirrors the master-lookup guard the
	// Delete override path carries (issue #412).
	if oErr != nil && !errors.Is(oErr, sql.ErrNoRows) {
		return wrapLookup("get override", oErr)
	}
	if oErr == nil {
		if err := qtx.SoftDeleteEvent(ctx, override.ID); err != nil {
			return fmt.Errorf("soft-delete override: %w", err)
		}
	}

	// Log the EXDATE-based delete so the trash view can surface it.
	// ON CONFLICT upserts deleted_at, so deleting the same instance twice
	// keeps exactly one log row with the latest timestamp.
	if err := qtx.RecordEventExdateDelete(ctx, storage.RecordEventExdateDeleteParams{
		CalendarID:   master.CalendarID,
		Uid:          uid,
		RecurrenceID: recID,
	}); err != nil {
		return fmt.Errorf("record exdate delete: %w", err)
	}

	// Mark the master dirty — its EXDATE was modified — inside the same
	// transaction so a failed mark rolls the EXDATE change back rather than
	// committing a change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "event"); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return tx.Commit()
}

// DeleteFromInstance truncates a recurring series so that instances at or
// after instanceTime are removed. It sets UNTIL on the RRULE. It soft-deletes
// any overrides at or after the cutoff. It records the pre-truncation
// RRULE in event_truncate_deletes so the trash view can restore it atomically.
// calendarID names the calendar of the series (see DeleteInstance).
func (s *Service) DeleteFromInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) error {
	_, err := s.deleteFromInstance(ctx, calendarID, uid, instanceTime)
	return err
}

// HasLiveOverrideFrom reports whether any live override of the series of
// uid in calendarID has a RECURRENCE-ID at or after from. The --following
// scope guard uses it: a truncation removes such overrides even when an
// imported EXDATE hides every generated slot, so their presence keeps the
// scope meaningful. The overrides of a master in another calendar stay
// out (issue #842): the truncation does not hide them, so they do not
// make the scope meaningful.
func (s *Service) HasLiveOverrideFrom(ctx context.Context, calendarID int64, uid string, from time.Time) (bool, error) {
	rows, err := s.q.ListSeriesOverridesInCalendar(ctx, storage.ListSeriesOverridesInCalendarParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		at, err := timeutil.ParseRecurrenceID(r.RecurrenceID)
		if err != nil {
			continue
		}
		if !at.Before(from) {
			return true, nil
		}
	}
	return false, nil
}

// softDeleteOverridesAndRecordTruncation trims the master's post-cutoff RDATEs.
// It hides every live override at or after cutoff. It records the truncation
// so the trash view can restore it. It captures the recurrence_ids it hides
// and the RDATEs it drops BEFORE it removes them. Restore then re-shows
// exactly those overrides and re-adds exactly those RDATEs.
//
// It does not restore an override the user deleted on its own (issue #287).
// It does not leave a post-cutoff RDATE to reappear on the next expansion
// (issue #463, since rrule-go expands RDATEs independently of the RRULE UNTIL
// bound). Pairs with restoreTruncatedOverrides / restoreTruncatedRDates.
//
// prevRRule is the master's pre-truncation RRULE. The caller passes it because
// it overwrote the master's recurrence_rule in the DB before this runs.
func softDeleteOverridesAndRecordTruncation(ctx context.Context, qtx *storage.Queries, master storage.Event, instanceTime time.Time, prevRRule string) error {
	uid := master.Uid
	cutoff := instanceTime.UTC().Format(time.RFC3339)

	removedRDates, err := trimMasterRDatesAtOrAfter(ctx, qtx, master, instanceTime)
	if err != nil {
		return err
	}

	// The overrides of the series in the calendar of the master. The rows
	// of a master in another calendar stay (issue #842).
	hidden, err := qtx.ListLiveSeriesOverrideRecurrenceIDsAtOrAfter(ctx, storage.ListLiveSeriesOverrideRecurrenceIDsAtOrAfterParams{
		CalendarID:   master.CalendarID,
		Uid:          uid,
		RecurrenceID: cutoff,
	})
	if err != nil {
		return fmt.Errorf("list future overrides: %w", err)
	}
	hiddenList := strings.Join(hidden, ",")

	if err := qtx.SoftDeleteSeriesOverridesAtOrAfter(ctx, storage.SoftDeleteSeriesOverridesAtOrAfterParams{
		CalendarID:   master.CalendarID,
		Uid:          uid,
		RecurrenceID: cutoff,
	}); err != nil {
		return fmt.Errorf("soft-delete future overrides: %w", err)
	}

	if err := qtx.RecordEventTruncateDelete(ctx, storage.RecordEventTruncateDeleteParams{
		CalendarID:      master.CalendarID,
		Uid:             uid,
		CutoffTime:      cutoff,
		PreviousRrule:   prevRRule,
		HiddenOverrides: &hiddenList,
		RemovedRdates:   &removedRDates,
	}); err != nil {
		return fmt.Errorf("record truncate: %w", err)
	}
	return nil
}

// trimMasterRDatesAtOrAfter rewrites the master's rdates column to keep only the
// occurrences strictly before instanceTime, and returns the dropped ones
// serialized for the truncation log. It only issues an UPDATE when something
// changes, so a master with no post-cutoff RDATEs is left untouched.
func trimMasterRDatesAtOrAfter(ctx context.Context, qtx *storage.Queries, master storage.Event, instanceTime time.Time) (string, error) {
	rdates := ParseTimeList(storage.NullableToString(master.Rdates))
	if len(rdates) == 0 {
		return "", nil
	}
	kept := make([]time.Time, 0, len(rdates))
	var removed []time.Time
	for _, rd := range rdates {
		if rd.Before(instanceTime) {
			kept = append(kept, rd)
		} else {
			removed = append(removed, rd)
		}
	}
	if len(removed) == 0 {
		return "", nil
	}
	if err := qtx.UpdateEventRdates(ctx, storage.UpdateEventRdatesParams{
		Rdates: storage.StringToNullable(SerializeTimeList(kept)),
		ID:     master.ID,
	}); err != nil {
		return "", fmt.Errorf("trim rdates: %w", err)
	}
	return SerializeTimeList(removed), nil
}

// deleteFromInstance performs the truncation and returns the master's
// updated_at as written by this operation, read back inside the same
// transaction. The in-tx value (rather than a post-commit read) closes a
// TOCTOU window. A concurrent writer that edits the master after our commit
// but before a separate read would otherwise have its updated_at captured as
// the undo baseline. RestoreUndo would then clobber that edit.
func (s *Service) deleteFromInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Read the master inside the transaction so the RRULE we truncate reflects
	// a concurrent writer's edits rather than a pre-transaction snapshot
	// (issue #116).
	master, _, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return "", wrapMasterLookup(err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return "", err
	}

	// An RDATE-only master (no RRULE) has no recurrence rule to truncate.
	// Synthesizing an "UNTIL=..." here would be an invalid bare RRULE that fails
	// to parse, collapsing the whole series to its DTSTART (issue #414). Leave
	// the rule NULL; the future-override soft-delete and truncation record below
	// still apply.
	prevRRule := storage.NullableToString(master.RecurrenceRule)
	if prevRRule != "" {
		until := instanceTime.UTC().Add(-time.Second)
		rule := setRRuleUntil(prevRRule, until, master.AllDay == 1)
		if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
			RecurrenceRule: storage.StringToNullable(rule),
			ID:             master.ID,
		}); err != nil {
			return "", fmt.Errorf("update rrule: %w", err)
		}
	}

	if err := softDeleteOverridesAndRecordTruncation(ctx, qtx, master, instanceTime, prevRRule); err != nil {
		return "", err
	}

	// Read the master's updated_at back inside the transaction so the value we
	// return reflects exactly this truncation's write, with no chance of an
	// interleaved external edit in between.
	truncated, err := qtx.GetEvent(ctx, master.ID)
	if err != nil {
		return "", fmt.Errorf("read back master: %w", err)
	}
	postUpdated := truncated.UpdatedAt

	// Mark the master dirty — its RRULE and RDATEs were modified — inside
	// the same transaction so a failed mark rolls the truncation back rather
	// than committing a change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "event"); err != nil {
		return "", fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return postUpdated, nil
}

// DeleteSeries deletes a recurring master event and all its overrides.
// calendarID names the calendar of the series. Two calendars can hold the
// same UID (issue #842), so the rows of the other calendar stay. With 0,
// one live master is the only choice, and more give ErrAmbiguousUID. See
// findSeriesMasterTx.
func (s *Service) DeleteSeries(ctx context.Context, calendarID int64, uid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Look up the master to get calendarID for tombstone creation. The
	// tombstone and the soft-delete commit together so a failed tombstone
	// write can't leave a tombstone for a still-live series whose next sync
	// would DELETE it from the server (issue #107).
	master, _, mErr := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	// Only ErrNoRows means "no master to track". A genuine lookup error must
	// abort. Otherwise the series is soft-deleted locally with no tombstone.
	// The next pull would then resurrect the series. This mirrors the guard
	// the todo and journal services already carry (issue #290).
	if mErr != nil && !errors.Is(mErr, sql.ErrNoRows) {
		return wrapMasterLookup(mErr)
	}
	if mErr == nil {
		calendarID = master.CalendarID
	}

	// The access guard covers the calendars of the series only. A
	// read-only copy of the UID in another calendar does not block the
	// delete (issue #842).
	if calendarID == 0 {
		if err := s.ensureSeriesWritable(ctx, uid); err != nil {
			return err
		}
	} else if err := ensureSeriesWritableIn(ctx, qtx, calendarID, uid); err != nil {
		return err
	}
	if mErr == nil {
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, master.CalendarID, uid); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
	}

	if calendarID == 0 {
		// No calendar and no live master: the UID holds override rows
		// only, so every row of the UID goes.
		if err := qtx.SoftDeleteEventsByUID(ctx, uid); err != nil {
			return fmt.Errorf("soft-delete series: %w", err)
		}
		return tx.Commit()
	}
	if err := qtx.SoftDeleteSeriesRowsInCalendar(ctx, storage.SoftDeleteSeriesRowsInCalendarParams{
		CalendarID: calendarID, Uid: uid,
	}); err != nil {
		return fmt.Errorf("soft-delete series: %w", err)
	}
	return tx.Commit()
}
