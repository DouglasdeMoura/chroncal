package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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

// ErrSeriesInTrash reports a series delete in a calendar whose master of
// the UID is in the trash, while another calendar holds a live master.
// The rows of that calendar include no live series, so the delete has no
// target. Restore the series first, or name the other calendar.
var ErrSeriesInTrash = errors.New("event: the series is in the trash in this calendar")

// UndoKind discriminates the three reversible delete shapes.
type UndoKind int

const (
	// UndoKindSingle is a standalone or single-override soft-delete. Restore
	// clears deleted_at on exactly one row.
	UndoKindSingle UndoKind = iota
	// UndoKindSeries is a full series soft-delete (DeleteSeries) in one
	// calendar. Restore un-hides the master of that calendar and the
	// override rows that the series-delete log names (see
	// restoreSeriesLogTx). A row of the UID in another calendar stays
	// as it is.
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

	// RowID is the override row that the delete hid, for an occurrence
	// delete with a stored override. The undo un-hides exactly that row.
	// 0 means no override row went to the trash with the delete.
	RowID int64

	// MasterID is the master row of the series at delete time. The undo
	// reverses the delete on that row, with no second look at the series
	// rule. 0 with RowIDs is a series delete of orphan rows.
	MasterID int64

	// RowIDs lists the rows that a series delete of orphan rows hid
	// (UndoKindSeries with MasterID 0). The undo un-hides exactly those rows and
	// drops them from the series-delete log. The rest of the log stays for the
	// restore of the master.
	RowIDs []int64

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
	rowID, masterID, calendarID := int64(0), evt.ID, evt.CalendarID
	if evt.RecurrenceID != "" {
		rowID = evt.ID
		// The master of the override at delete time. No master (an
		// orphan row) leaves 0, and the undo un-hides the row alone.
		// The undo key is the calendar of the master, the same key that
		// an occurrence delete of the row records. The row stays in
		// RowID.
		masterID = 0
		if master, err := findSeriesMasterIncludingDeletedTx(ctx, s.q, evt.CalendarID, evt.UID); err == nil {
			masterID = master.ID
			calendarID = master.CalendarID
		}
	}
	return UndoMeta{
		Kind:         UndoKindSingle,
		UID:          evt.UID,
		Label:        evt.Title,
		DeletedAt:    time.Now().UTC(),
		CalendarID:   calendarID,
		RecurrenceID: evt.RecurrenceID,
		RowID:        rowID,
		MasterID:     masterID,
	}, nil
}

// DeleteInstanceWithUndo excludes an occurrence and returns undo metadata.
// The overridden instance (if any) is soft-deleted; on Restore we un-hide it
// and remove the EXDATE we added. calendarID names the calendar of the
// series (see findSeriesMasterTx).
func (s *Service) DeleteInstanceWithUndo(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (UndoMeta, error) {
	master, rowID, err := s.deleteInstance(ctx, calendarID, uid, instanceTime)
	if err != nil {
		return UndoMeta{}, err
	}
	return UndoMeta{
		Kind:         UndoKindSingle,
		UID:          uid,
		Label:        master.Title,
		DeletedAt:    time.Now().UTC(),
		CalendarID:   master.CalendarID,
		RecurrenceID: instanceTime.UTC().Format(time.RFC3339),
		RowID:        rowID,
		MasterID:     master.ID,
	}, nil
}

// DeleteFromInstanceWithUndo truncates the series at instanceTime and returns
// the cutoff plus master UpdatedAt. Restore can then reverse the truncation
// exactly via the event_truncate_deletes log row. calendarID names the
// calendar of the series (see findSeriesMasterTx).
func (s *Service) DeleteFromInstanceWithUndo(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (UndoMeta, error) {
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
	master, postUpdated, err := s.deleteFromInstance(ctx, calendarID, uid, instanceTime)
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
	d, err := s.deleteSeries(ctx, calendarID, uid)
	if err != nil {
		return UndoMeta{}, err
	}
	return UndoMeta{
		Kind:       UndoKindSeries,
		UID:        uid,
		Label:      d.Label,
		DeletedAt:  time.Now().UTC(),
		CalendarID: d.CalendarID,
		MasterID:   d.Master.ID,
		RowIDs:     d.Hidden,
	}, nil
}

// RestoreUndo reverses a soft-delete operation recorded in UndoMeta. It
// dispatches by Kind. For FromInstance kinds, it also rewrites the master's
// RRULE back to the pre-truncation value in the same transaction.
func (s *Service) RestoreUndo(ctx context.Context, meta UndoMeta) error {
	// The guard covers the calendars of the series only. A calendar
	// without a row of the series, for example a read-only copy of the
	// UID, does not block the restore (issue #842).
	if err := ensureSeriesWritableIn(ctx, s.q, meta.CalendarID, meta.UID); err != nil {
		return err
	}
	switch meta.Kind {
	case UndoKindSingle:
		return s.restoreSingle(ctx, meta)
	case UndoKindSeries:
		return s.restoreSeries(ctx, meta)
	case UndoKindFromInstance:
		return s.restoreFromInstance(ctx, meta)
	default:
		return fmt.Errorf("unknown undo kind %d", meta.Kind)
	}
}

// RestoreByUID reverses a series delete of uid. Used by the CLI `events
// restore <uid>` path. A series delete leaves its master in the trash, or
// a series-delete log row (event_series_deletes), so those calendars are
// the candidates. A live series with rows in the trash for another reason
// is no candidate. An occurrence that the user deleted on its own, or a
// row that a truncation hid, keeps its own trash entry (issue #287).
//
// One candidate is the target. Two calendars can hold the same UID (issue
// #842), so more candidates give ErrAmbiguousUID: restore the series by
// the ID of its master instead. No candidate gives ErrNotDeleted. A UID
// with no master row and no log holds override rows only. They all come
// back, as before the logs.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	rows, err := s.q.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return fmt.Errorf("list masters: %w", err)
	}
	logs, err := s.q.ListEventSeriesDeletesByUID(ctx, uid)
	if err != nil {
		return fmt.Errorf("list series delete logs: %w", err)
	}
	if len(rows) == 0 && len(logs) == 0 {
		if err := ensureSeriesWritableIn(ctx, s.q, 0, uid); err != nil {
			return err
		}
		calendars, err := s.q.RestoreEventsByUID(ctx, uid)
		if err != nil {
			return fmt.Errorf("restore by uid: %w", err)
		}
		if len(calendars) == 0 {
			return ErrNotDeleted
		}
		return s.reconcileRestoredCalendars(ctx, uid, calendars)
	}
	calendarID, holdsMaster, err := restoreCandidate(rows, logs)
	if err != nil {
		return err
	}
	if err := ensureSeriesWritableIn(ctx, s.q, calendarID, uid); err != nil {
		return err
	}
	n, err := s.restoreSeriesLog(ctx, calendarID, uid, holdsMaster)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotDeleted
	}
	return s.reconcileSyncAfterRestore(ctx, calendarID, uid)
}

// restoreCandidate chooses the calendar whose series delete RestoreByUID
// reverses. holdsMaster reports whether that calendar holds a master row
// of the UID. The candidates have two ranks:
//  1. A calendar with a series-delete log of the UID. The log records a
//     series delete that no restore consumed.
//  2. A calendar with a master in the trash and no log, for example an
//     old copy whose log the purge dropped.
//
// The highest rank with a candidate decides. One candidate there is the
// result. Two or more give ErrAmbiguousUID. No candidate gives
// ErrNotDeleted.
func restoreCandidate(masters []storage.Event, logs []storage.EventSeriesDelete) (calendarID int64, holdsMaster bool, err error) {
	holds := make(map[int64]bool, len(masters))
	var trashed []int64
	for _, m := range masters {
		holds[m.CalendarID] = true
		if m.DeletedAt != nil {
			trashed = append(trashed, m.CalendarID)
		}
	}
	ranked := make([]int64, 0, len(logs))
	for _, l := range logs {
		ranked = append(ranked, l.CalendarID)
	}
	if len(ranked) == 0 {
		ranked = trashed
	}
	switch len(ranked) {
	case 0:
		return 0, false, ErrNotDeleted
	case 1:
		return ranked[0], holds[ranked[0]], nil
	}
	return 0, false, ErrAmbiguousUID
}

// reconcileRestoredCalendars reconciles the sync state of each calendar in
// calendars for uid. The restore of a UID with no master row passes the
// calendars where rows came back. Without this, a pending tombstone
// survives the restore. The next sync would then DELETE the series from
// the server right after a successful restore. A calendar where no row
// came back keeps its sync state.
func (s *Service) reconcileRestoredCalendars(ctx context.Context, uid string, calendars []int64) error {
	done := make(map[int64]bool, len(calendars))
	for _, id := range calendars {
		if done[id] {
			continue
		}
		done[id] = true
		if err := s.reconcileSyncAfterRestore(ctx, id, uid); err != nil {
			return err
		}
	}
	return nil
}

// RestoreByID un-hides a soft-deleted row. Used by the CLI `events restore
// <id>` path. A master row reverses the series delete: it brings back the
// overrides that the series delete hid. An occurrence that the user deleted
// on its own before stays deleted, with its EXDATE (issue #287). Two
// calendars can hold the same UID (issue #842). The ID of the master is then
// the one reference that names one series in the trash. An override row comes
// back alone. Reconciles sync state so subsequent pushes re-CREATE the
// resource on the server if necessary.
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
	if r.RecurrenceID != "" {
		if err := calendaraccess.EnsureWritable(ctx, s.q, r.CalendarID, "VEVENT"); err != nil {
			return err
		}
		if err := s.restoreOverrideByID(ctx, r); err != nil {
			return err
		}
		if s.masterInTrash(ctx, r.CalendarID, r.Uid) {
			// The row stays local until the master comes back. The
			// tombstone of the series stays, and the UID gets no dirty
			// mark: a push would PUT the override alone over the server
			// object of the whole series.
			return nil
		}
		return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
	}
	if err := ensureSeriesWritableIn(ctx, s.q, r.CalendarID, r.Uid); err != nil {
		return err
	}
	if _, err := s.restoreSeriesLog(ctx, r.CalendarID, r.Uid, true); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
}

// recordSeriesDeleteTx writes the series-delete log of (calendarID, uid)
// with hidden. merge keeps the rows that an unconsumed log row of the
// same key already names. Only a delete of orphan rows beside a master in
// the trash merges: that master still needs its log. A delete with a live
// master writes a fresh list. A log row from before the master came back
// by a path that keeps logs (a pull, an import) is stale (issue #287).
func recordSeriesDeleteTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, hidden []int64, merge bool) error {
	if merge {
		log, err := qtx.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{
			CalendarID: calendarID, Uid: uid,
		})
		found, err := storage.Found(err)
		if err != nil {
			return fmt.Errorf("get series delete log: %w", err)
		}
		if found {
			earlier, err := parseRowIDs(log.HiddenIds)
			if err != nil {
				return err
			}
			seen := make(map[int64]bool, len(hidden))
			for _, id := range hidden {
				seen[id] = true
			}
			for _, id := range earlier {
				if !seen[id] {
					hidden = append(hidden, id)
				}
			}
		}
	}
	if err := qtx.RecordEventSeriesDelete(ctx, storage.RecordEventSeriesDeleteParams{
		CalendarID: calendarID, Uid: uid, HiddenIds: formatRowIDs(hidden),
	}); err != nil {
		return fmt.Errorf("record series delete: %w", err)
	}
	return nil
}

// restoreSeriesLog runs restoreSeriesLogTx in one transaction.
func (s *Service) restoreSeriesLog(ctx context.Context, calendarID int64, uid string, requireMaster bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	n, err := restoreSeriesLogTx(ctx, s.q.WithTx(tx), calendarID, uid, requireMaster)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// restoreSeriesLogTx reverses a series delete in one calendar. It
// un-hides the master and the override rows that the series delete hid.
// The series-delete log row (event_series_deletes) lists those rows by
// ID. The restore touches exactly that set, also when the owner of a
// legacy override changed after the delete. The calendar of each hidden
// row takes the write. The restore consumes the log row.
//
// A master with no log row comes back with the override rows that went to
// the trash in the same statement (sameDeleteRowsTx). Three deletes leave
// no log row:
//
//   - a plain delete of a master,
//   - a delete that a sync pull made,
//   - a series delete from before the log (migration 057).
//
// An override that a sync prune, an occurrence delete, or a truncation
// hid stays in the trash with its EXDATE and its log row. The trash view
// still lists it on its own.
//
// requireMaster (the reverse of a series delete with a master) also
// un-hides the master of calendarID. A calendar with no master row is
// then an error. Without it (the undo of a delete of orphan rows), it never
// touches a master: that delete hid none. The count is the number of rows
// that came back.
func restoreSeriesLogTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, requireMaster bool) (int64, error) {
	var master storage.Event
	if requireMaster {
		var err error
		master, err = qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
			CalendarID: calendarID, Uid: uid,
		})
		if err != nil {
			return 0, fmt.Errorf("get master: %w", err)
		}
	}
	// The log comes first: the revival of the master fires the trigger
	// that drops the log of its calendar (migration 057). The log row
	// goes before the restore, so the drop of the restored rows from the
	// other logs does not rewrite it either.
	log, err := qtx.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{
		CalendarID: calendarID, Uid: uid,
	})
	logged, err := storage.Found(err)
	if err != nil {
		return 0, fmt.Errorf("get series delete log: %w", err)
	}
	var ids []int64
	switch {
	case logged:
		ids, err = parseRowIDs(log.HiddenIds)
		if err != nil {
			return 0, err
		}
		if err := qtx.DeleteEventSeriesDeletesByCalendarUID(ctx, storage.DeleteEventSeriesDeletesByCalendarUIDParams{
			CalendarID: calendarID, Uid: uid,
		}); err != nil {
			return 0, fmt.Errorf("delete series delete log: %w", err)
		}
	case requireMaster && master.DeletedAt != nil:
		ids, err = sameDeleteRowsTx(ctx, qtx, master)
		if err != nil {
			return 0, err
		}
	}
	var n int64
	if requireMaster && master.DeletedAt != nil {
		k, err := qtx.RestoreEventsByIDs(ctx, []int64{master.ID})
		if err != nil {
			return 0, fmt.Errorf("restore master: %w", err)
		}
		n += k
	}
	k, err := restoreLoggedIDsTx(ctx, qtx, calendarID, uid, ids)
	if err != nil {
		return 0, err
	}
	return n + k, nil
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
	r, err := s.q.GetEventIncludingDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get event: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	n, err := qtx.PurgeEventByID(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotDeleted
	}
	// The occurrence logs of a series go with its master: with the master gone,
	// no restore can use them. The series-delete log stays. The undo of the
	// series delete still reverses it on the orphan rows (restoreSeries), and a
	// move drops or merges a stale log row.
	if r.RecurrenceID == "" {
		if err := deleteOccurrenceLogRowsTx(ctx, qtx, r.CalendarID, r.Uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// restoreSingle reverses one single-row delete from its record: the
// master that meta.MasterID names, and the override row that meta.RowID
// names. Undo data with no master and no row names no delete, so it gives
// ErrNotDeleted.
func (s *Service) restoreSingle(ctx context.Context, meta UndoMeta) error {
	calendarID, uid, recurrenceID, rowID := meta.CalendarID, meta.UID, meta.RecurrenceID, meta.RowID
	r, err := s.undoMaster(ctx, meta)
	if errors.Is(err, ErrNotDeleted) {
		// No master: an orphan row, or a master that a purge took. The
		// delete of an override still has its own record, the row.
		if rowID == 0 {
			return err
		}
		if err := s.restoreOverrideRow(ctx, rowID); err != nil {
			return err
		}
		return s.reconcileSyncAfterRestore(ctx, calendarID, uid)
	}
	if err != nil {
		return err
	}
	// The restore of a legacy override writes the EXDATE of its master,
	// so the calendar of the master takes the write too.
	if r.CalendarID != calendarID {
		if err := calendaraccess.EnsureWritable(ctx, s.q, r.CalendarID, "VEVENT"); err != nil {
			return err
		}
	}
	if r.DeletedAt != nil && recurrenceID != "" {
		// The master is in the trash, and the undo reverses the delete of
		// one occurrence. The undo un-hides the row of the delete, never
		// the master. The EXDATE of the occurrence goes from the trashed
		// master.
		// The row stays local until the master comes back, as a restore
		// by ID beside a trashed master does: no sync reconcile.
		return s.restoreOverrideOfTrashedMaster(ctx, r, recurrenceID, rowID)
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		if recurrenceID != "" {
			return s.restoreEXDATEOnly(ctx, r.CalendarID, uid, recurrenceID, rowID)
		}
		// The undo reverses a delete of the master row, and the master
		// is live: a restore from the trash came first. The undo has
		// nothing to do. It must not un-hide the other rows of the
		// series: an occurrence deleted on its own stays deleted
		// (issue #287).
		return ErrNotDeleted
	}
	if err := s.q.RestoreEvent(ctx, r.ID); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
}

// restoreLoggedIDsTx un-hides the rows that a delete log names: the
// series-delete log and the truncation log use it. It guards the
// calendar of each row, un-hides the rows in one statement, and drops
// them from every other log of the UID. It returns the number of rows
// that came back.
func restoreLoggedIDsTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if err := ensureRowsWritableTx(ctx, qtx, calendarID, ids); err != nil {
		return 0, err
	}
	n, err := qtx.RestoreEventsByIDs(ctx, ids)
	if err != nil {
		return 0, fmt.Errorf("restore logged rows: %w", err)
	}
	if err := dropHiddenRowsTx(ctx, qtx, uid, ids); err != nil {
		return 0, err
	}
	return n, nil
}

// parseRowIDs parses a comma-separated list of event row IDs from a
// delete log. An empty list gives no IDs.
func parseRowIDs(list string) ([]int64, error) {
	if list == "" {
		return nil, nil
	}
	fields := strings.Split(list, ",")
	ids := make([]int64, 0, len(fields))
	for _, field := range fields {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse hidden row id %q: %w", field, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ensureRowsWritableTx enforces the remote access policy on the calendar of
// each row of ids. It skips calendarID, the calendar of the series, which the
// caller guards. A legacy override lives in another calendar than its master,
// and its restore writes that calendar.
func ensureRowsWritableTx(ctx context.Context, qtx *storage.Queries, calendarID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	// One query reads the calendars of the rows. A purged row is not in
	// the result, so it needs no guard.
	calendars, err := qtx.ListEventCalendarIDsByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("list calendars of hidden rows: %w", err)
	}
	for _, id := range calendars {
		if id == calendarID {
			continue
		}
		if err := calendaraccess.EnsureWritable(ctx, qtx, id, "VEVENT"); err != nil {
			return err
		}
	}
	return nil
}

// formatRowIDs is the inverse of parseRowIDs.
func formatRowIDs(ids []int64) string {
	fields := make([]string, len(ids))
	for i, id := range ids {
		fields[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(fields, ",")
}

// dropHiddenRowsTx removes ids from every delete log of uid that names them:
// the series-delete logs and the truncation logs. A restore of logged rows by
// ID or by undo calls it. A later restore of the series or of the truncation
// then does not reverse an independent delete of a row (issue #287).
func dropHiddenRowsTx(ctx context.Context, qtx *storage.Queries, uid string, ids []int64) error {
	seriesLogs, err := qtx.ListEventSeriesDeletesByUID(ctx, uid)
	if err != nil {
		return fmt.Errorf("list series delete logs: %w", err)
	}
	for _, log := range seriesLogs {
		kept, changed, err := removeRowIDs(log.HiddenIds, ids)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		if kept == "" {
			// The log names no row any more, so the restore of the
			// series has nothing to read from it.
			if err := qtx.DeleteEventSeriesDeletesByCalendarUID(ctx, storage.DeleteEventSeriesDeletesByCalendarUIDParams{CalendarID: log.CalendarID, Uid: log.Uid}); err != nil {
				return fmt.Errorf("delete series delete log: %w", err)
			}
			continue
		}
		if err := qtx.UpdateEventSeriesDeleteHiddenIds(ctx, storage.UpdateEventSeriesDeleteHiddenIdsParams{HiddenIds: kept, ID: log.ID}); err != nil {
			return fmt.Errorf("update series delete log: %w", err)
		}
	}
	truncLogs, err := qtx.ListEventTruncateDeletesByUID(ctx, uid)
	if err != nil {
		return fmt.Errorf("list truncate logs: %w", err)
	}
	for _, log := range truncLogs {
		if log.HiddenIds == nil {
			continue
		}
		kept, changed, err := removeRowIDs(*log.HiddenIds, ids)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		if err := qtx.UpdateEventTruncateDeleteHiddenIds(ctx, storage.UpdateEventTruncateDeleteHiddenIdsParams{HiddenIds: &kept, ID: log.ID}); err != nil {
			return fmt.Errorf("update truncate log: %w", err)
		}
	}
	return nil
}

// removeRowIDs drops ids from a comma-separated list of row IDs. changed
// reports whether the list held any of them.
func removeRowIDs(list string, ids []int64) (string, bool, error) {
	all, err := parseRowIDs(list)
	if err != nil {
		return "", false, err
	}
	drop := make(map[int64]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	kept := make([]int64, 0, len(all))
	for _, candidate := range all {
		if !drop[candidate] {
			kept = append(kept, candidate)
		}
	}
	return formatRowIDs(kept), len(kept) != len(all), nil
}

// restoreOverrideRow un-hides the override row of an undo with no master,
// by its ID. It drops the row from the delete logs of the UID. No row, or
// a live row, gives ErrNotDeleted.
func (s *Service) restoreOverrideRow(ctx context.Context, rowID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := restoreRowByIDTx(ctx, s.q.WithTx(tx), rowID); err != nil {
		return err
	}
	return tx.Commit()
}

// restoreRowByIDTx un-hides one row by ID and drops it from the delete
// logs of its UID. No row, or a live row, gives ErrNotDeleted.
func restoreRowByIDTx(ctx context.Context, qtx *storage.Queries, rowID int64) error {
	n, err := qtx.RestoreEventsByIDs(ctx, []int64{rowID})
	if err != nil {
		return fmt.Errorf("restore row %d: %w", rowID, err)
	}
	if n == 0 {
		return ErrNotDeleted
	}
	row, err := qtx.GetEventIncludingDeleted(ctx, rowID)
	if err != nil {
		return fmt.Errorf("get row %d: %w", rowID, err)
	}
	return dropHiddenRowsTx(ctx, qtx, row.Uid, []int64{rowID})
}

// undoMaster reads the master row that meta.MasterID names, live or in the
// trash. No master in the undo data, or a purged master, gives
// ErrNotDeleted. The series rule never picks another master.
func (s *Service) undoMaster(ctx context.Context, meta UndoMeta) (storage.Event, error) {
	if meta.MasterID == 0 {
		return storage.Event{}, ErrNotDeleted
	}
	r, err := s.q.GetEventIncludingDeleted(ctx, meta.MasterID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Event{}, ErrNotDeleted
	}
	return r, err
}

// masterInTrash reports whether the series that owns the rows of uid in
// calendarID has its master in the trash. A lookup error counts as no.
func (s *Service) masterInTrash(ctx context.Context, calendarID int64, uid string) bool {
	m, err := findSeriesMasterIncludingDeletedTx(ctx, s.q, calendarID, uid)
	return err == nil && m.DeletedAt != nil
}

// restoreOverrideOfTrashedMaster reverses an occurrence delete of the
// series of master, which is in the trash. It un-hides the override row
// that the delete hid (rowID, 0 with no stored override). It clears the
// EXDATE of the occurrence on the trashed master.
func (s *Service) restoreOverrideOfTrashedMaster(ctx context.Context, master storage.Event, recurrenceID string, rowID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if rowID != 0 {
		if err := restoreRowByIDTx(ctx, qtx, rowID); err != nil {
			return err
		}
	}
	if err := clearMasterEXDATEIncludingDeleted(ctx, qtx, master.CalendarID, master.Uid, recurrenceID); err != nil {
		return err
	}
	return tx.Commit()
}

// restoreSeries reverses a series delete from its own record. With MasterID,
// it un-hides that master and the rows of the series-delete log of its
// calendar. With RowIDs (a delete of orphan rows), it un-hides exactly those
// rows and drops them from the log. The rest of the log stays for the restore
// of the master. A purged master, a master that a restore from the trash
// brought back first, or rows that are live again give ErrNotDeleted. Undo
// data with neither field names no delete. An occurrence that the user
// deleted on its own before the series delete stays deleted (issue #287, see
// restoreSeriesLogTx).
func (s *Service) restoreSeries(ctx context.Context, meta UndoMeta) error {
	uid := meta.UID
	switch {
	case meta.MasterID != 0:
		master, err := s.q.GetEventIncludingDeleted(ctx, meta.MasterID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// The master is purged on its own. The log of the delete
			// still names the hidden rows (an orphan tail), so the
			// undo reverses that record. The series rule stays out.
			n, err := s.restoreSeriesLog(ctx, meta.CalendarID, uid, false)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrNotDeleted
			}
			return s.reconcileSyncAfterRestore(ctx, meta.CalendarID, uid)
		case err != nil:
			return fmt.Errorf("get master: %w", err)
		case master.DeletedAt == nil:
			return ErrNotDeleted
		}
		if _, err := s.restoreSeriesLog(ctx, master.CalendarID, uid, true); err != nil {
			return err
		}
		return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
	case len(meta.RowIDs) != 0:
		n, err := s.restoreRows(ctx, meta.CalendarID, uid, meta.RowIDs)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotDeleted
		}
		return s.reconcileSyncAfterRestore(ctx, meta.CalendarID, uid)
	default:
		return fmt.Errorf("%w: the undo data names no master and no rows", ErrNotDeleted)
	}
}

// restoreRows un-hides the rows of ids, which belong to uid in the series
// of calendarID, in one guarded statement (restoreLoggedIDsTx). It drops
// them from the delete logs of the UID and returns the number of rows
// that came back.
func (s *Service) restoreRows(ctx context.Context, calendarID int64, uid string, ids []int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	n, err := restoreLoggedIDsTx(ctx, s.q.WithTx(tx), calendarID, uid, ids)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Service) restoreOverrideByID(ctx context.Context, r storage.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := restoreRowByIDTx(ctx, qtx, r.ID); err != nil {
		return err
	}
	// The master of the override is in the calendar of the row. Else the row is
	// a legacy override of the one master of the UID. No master leaves no EXDATE
	// to clear. A legacy override of a UID with more than one master belongs to
	// no series. Delete recorded no EXDATE for it, so there is nothing to clear
	// either. The master can be in the trash: the clear then writes the trashed
	// master, so its own restore shows the occurrence again.
	master, err := findSeriesMasterIncludingDeletedTx(ctx, qtx, r.CalendarID, r.Uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrAmbiguousUID) {
		return fmt.Errorf("get master: %w", err)
	}
	if err == nil {
		// The EXDATE lives on the master, so its calendar takes the
		// write too. A legacy override lives in another calendar.
		if master.CalendarID != r.CalendarID {
			if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
				return err
			}
		}
		if err := clearMasterEXDATEIncludingDeleted(ctx, qtx, master.CalendarID, r.Uid, r.RecurrenceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// restoreEXDATEOnly reverses an occurrence delete of the series in
// calendarID. It un-hides the override row that the delete hid (rowID) and
// clears the EXDATE that the delete recorded. rowID 0 says the undo data
// names no row (an older UndoMeta). The restore then un-hides the override
// rows of the occurrence that the series owns.
func (s *Service) restoreEXDATEOnly(ctx context.Context, calendarID int64, uid, recurrenceID string, rowID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// DeleteInstance soft-deletes the override at this recurrence_id (if one
	// existed) and adds the EXDATE. Un-hide it so undo restores the
	// customized occurrence, not just the base instance. rowID 0 says the
	// delete hid no override. A row of the occurrence that an earlier
	// delete hid on its own stays in the trash.
	if rowID != 0 {
		if err := restoreRowByIDTx(ctx, qtx, rowID); err != nil && !errors.Is(err, ErrNotDeleted) {
			return err
		}
	}
	if err := clearMasterEXDATE(ctx, qtx, calendarID, uid, recurrenceID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, calendarID, uid)
}

// sameDeleteRowsTx lists the override rows of the series of master that
// went to the trash in the same statement as master. Two deletes leave no
// log: a series delete from before the series-delete log (migration 057),
// and the series delete of a pull. Each hides the master and its
// overrides in one statement, so the rows share deleted_at to the second. A row that an
// occurrence log or a truncation log names went in a delete of its own,
// and it stays in the trash.
func sameDeleteRowsTx(ctx context.Context, qtx *storage.Queries, master storage.Event) ([]int64, error) {
	trashed, err := qtx.ListTrashedSeriesRowsInCalendar(ctx, storage.ListTrashedSeriesRowsInCalendarParams{
		CalendarID: master.CalendarID, Uid: master.Uid,
	})
	if err != nil {
		return nil, fmt.Errorf("list trashed series rows: %w", err)
	}
	truncated, err := truncationHidden(ctx, qtx, master.CalendarID, master.Uid)
	if err != nil {
		return nil, err
	}
	exdateLogs, err := qtx.ListEventExdateDeletesByCalendarUID(ctx, storage.ListEventExdateDeletesByCalendarUIDParams{
		CalendarID: master.CalendarID, Uid: master.Uid,
	})
	if err != nil {
		return nil, fmt.Errorf("list exdate logs: %w", err)
	}
	ownDeletes := make(map[string]bool, len(exdateLogs))
	for _, l := range exdateLogs {
		ownDeletes[l.RecurrenceID] = true
	}
	var ids []int64
	for _, r := range trashed {
		if r.RecurrenceID == "" || r.DeletedAt == nil || *r.DeletedAt != *master.DeletedAt || truncated(r) || ownDeletes[r.RecurrenceID] {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// truncationHidden returns a test for the rows that the unconsumed
// truncation logs of the series in calendarID hid. A log row with row IDs
// names its rows. A log row from before the IDs (migration 057) names
// recurrence IDs in hidden_overrides. A log row from before issue #287
// names no rows: the rows at or after its cutoff count.
func truncationHidden(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (func(storage.Event) bool, error) {
	logs, err := qtx.ListEventTruncateDeletesByCalendarUID(ctx, storage.ListEventTruncateDeletesByCalendarUIDParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return nil, fmt.Errorf("list truncate logs: %w", err)
	}
	ids := make(map[int64]bool)
	recIDs := make(map[string]bool)
	var cutoffs []string
	for _, log := range logs {
		switch {
		case log.HiddenIds != nil:
			parsed, err := parseRowIDs(*log.HiddenIds)
			if err != nil {
				return nil, err
			}
			for _, id := range parsed {
				ids[id] = true
			}
		case log.HiddenOverrides != nil:
			for _, recID := range strings.Split(*log.HiddenOverrides, ",") {
				if recID != "" {
					recIDs[recID] = true
				}
			}
		default:
			cutoffs = append(cutoffs, log.CutoffTime)
		}
	}
	return func(r storage.Event) bool {
		if ids[r.ID] {
			return true
		}
		if r.RecurrenceID == "" {
			return false
		}
		if recIDs[r.RecurrenceID] {
			return true
		}
		for _, cutoff := range cutoffs {
			if r.RecurrenceID >= cutoff {
				return true
			}
		}
		return false
	}, nil
}

// clearMasterEXDATE reverses the EXDATE an instance-delete added for
// recurrenceID on the live master event with uid in calendarID. The
// provenance contract lives in softdelete.ClearMasterEXDATE; this wrapper
// only binds the event queries to the active transaction so the override
// is never visible-but-excluded. The delete log and the master are per
// calendar: two calendars can hold the same UID (issue #842).
func clearMasterEXDATE(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recurrenceID string) error {
	return clearMasterEXDATEWith(ctx, qtx, calendarID, uid, recurrenceID, false)
}

// clearMasterEXDATEIncludingDeleted is clearMasterEXDATE for a master that
// can be in the trash. The restore of an override by ID uses it. With a
// live-only lookup, the contract would read a trashed master as gone and drop
// the log row. The EXDATE would then stay after the master restore.
func clearMasterEXDATEIncludingDeleted(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recurrenceID string) error {
	return clearMasterEXDATEWith(ctx, qtx, calendarID, uid, recurrenceID, true)
}

func clearMasterEXDATEWith(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recurrenceID string, includeDeleted bool) error {
	return softdelete.ClearMasterEXDATE(ctx, softdelete.ExdateProvenance{
		GetDeleteLog: func(ctx context.Context) (int64, bool, error) {
			log, err := qtx.GetEventExdateDeleteByCalendarUIDRecurrence(ctx, storage.GetEventExdateDeleteByCalendarUIDRecurrenceParams{
				CalendarID: calendarID, Uid: uid, RecurrenceID: recurrenceID,
			})
			found, err := storage.Found(err)
			return log.ID, found, err
		},
		GetMaster: func(ctx context.Context) (int64, string, bool, error) {
			var master storage.Event
			var err error
			if includeDeleted {
				master, err = qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{CalendarID: calendarID, Uid: uid})
			} else {
				master, err = qtx.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
			}
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
	master, err := findSeriesMasterIncludingDeletedTx(ctx, s.q, meta.CalendarID, meta.UID)
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

		// The master is in the calendar of the override, or the override is a
		// legacy override of the one live master of the UID. A legacy override of a
		// UID with more than one live master belongs to no series. The expansion
		// keys the overrides by calendar, so no master shows it. The row goes
		// without EXDATE bookkeeping, like a row with no master.
		master, err := findSeriesMasterTx(ctx, qtx, evt.CalendarID, evt.UID)
		// A genuine lookup error (e.g. SQLITE_BUSY) must not collapse into the
		// "no master" path: that would soft-delete the override while skipping
		// its EXDATE/provenance bookkeeping, resurrecting the occurrence via
		// series expansion (issue #412). Only a missing master (ErrNoRows)
		// legitimately skips the bookkeeping.
		if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrAmbiguousUID) {
			return fmt.Errorf("get master: %w", err)
		}
		if err == nil {
			// The EXDATE and the delete log go onto the master, so its
			// calendar takes the write too. A legacy override lives in
			// another calendar than its master.
			if master.CalendarID != evt.CalendarID {
				if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
					return err
				}
			}
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
	_, _, err := s.deleteInstance(ctx, calendarID, uid, instanceTime)
	return err
}

// deleteInstance is DeleteInstance. It returns the master it read in the
// transaction. It also returns the ID of the override row that the delete
// hid, or 0 with no stored override.
func (s *Service) deleteInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (storage.Event, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Event{}, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Read the master inside the transaction so the EXDATE list we recompute
	// reflects a concurrent writer's changes (issue #116). Reading outside the
	// tx let a second instance-delete clobber the first one's EXDATE.
	master, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return storage.Event{}, 0, wrapMasterLookup(err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return storage.Event{}, 0, err
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
		return storage.Event{}, 0, fmt.Errorf("update exdates: %w", err)
	}

	recID := instanceTime.UTC().Format(time.RFC3339)
	override, oErr := findInstanceOverrideTx(ctx, qtx, master, recID, true)
	// Only ErrNoRows means "this occurrence has no override row". A genuine
	// lookup error must abort. Otherwise the EXDATE and its provenance commit
	// while the live override row stays. The deleted occurrence then stays
	// visible through the override. This mirrors the master-lookup guard the
	// Delete override path carries (issue #412).
	if oErr != nil && !errors.Is(oErr, sql.ErrNoRows) {
		return storage.Event{}, 0, wrapLookup("get override", oErr)
	}
	var hiddenID int64
	if oErr == nil {
		// A legacy override lives in another calendar than its master,
		// and the delete writes that calendar too.
		if override.CalendarID != master.CalendarID {
			if err := calendaraccess.EnsureWritable(ctx, qtx, override.CalendarID, "VEVENT"); err != nil {
				return storage.Event{}, 0, err
			}
		}
		if err := qtx.SoftDeleteEvent(ctx, override.ID); err != nil {
			return storage.Event{}, 0, fmt.Errorf("soft-delete override: %w", err)
		}
		hiddenID = override.ID
	}

	// Log the EXDATE-based delete so the trash view can surface it.
	// ON CONFLICT upserts deleted_at, so deleting the same instance twice
	// keeps exactly one log row with the latest timestamp.
	if err := qtx.RecordEventExdateDelete(ctx, storage.RecordEventExdateDeleteParams{
		CalendarID:   master.CalendarID,
		Uid:          uid,
		RecurrenceID: recID,
	}); err != nil {
		return storage.Event{}, 0, fmt.Errorf("record exdate delete: %w", err)
	}

	// Mark the master dirty — its EXDATE was modified — inside the same
	// transaction so a failed mark rolls the EXDATE change back rather than
	// committing a change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "event"); err != nil {
		return storage.Event{}, 0, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.Event{}, 0, err
	}
	return master, hiddenID, nil
}

// DeleteFromInstance truncates a recurring series so that instances at or
// after instanceTime are removed. It sets UNTIL on the RRULE. It soft-deletes
// any overrides at or after the cutoff. It records the pre-truncation
// RRULE in event_truncate_deletes so the trash view can restore it atomically.
// calendarID names the calendar of the series (see DeleteInstance).
func (s *Service) DeleteFromInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) error {
	_, _, err := s.deleteFromInstance(ctx, calendarID, uid, instanceTime)
	return err
}

// HasLiveOverrideFrom reports whether any live override of the series of uid
// in calendarID has a RECURRENCE-ID at or after from. The --following scope
// guard uses it. A truncation removes such overrides even when an imported
// EXDATE hides every generated slot, so their presence keeps the scope
// meaningful. The overrides of a master in another calendar stay out (issue
// #842). The truncation does not hide them, so they do not make the scope
// meaningful.
func (s *Service) HasLiveOverrideFrom(ctx context.Context, calendarID int64, uid string, from time.Time) (bool, error) {
	ids, err := s.q.ListLiveSeriesOverrideRecurrenceIDsAtOrAfter(ctx, storage.ListLiveSeriesOverrideRecurrenceIDsAtOrAfterParams{
		CalendarID:   calendarID,
		Uid:          uid,
		RecurrenceID: from.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
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
	hidden, err := qtx.ListLiveSeriesOverridesAtOrAfter(ctx, storage.ListLiveSeriesOverridesAtOrAfterParams{
		CalendarID:   master.CalendarID,
		Uid:          uid,
		RecurrenceID: cutoff,
	})
	if err != nil {
		return fmt.Errorf("list future overrides: %w", err)
	}
	// The log keeps the recurrence IDs for the trash view and the row IDs for
	// the restore (see restoreTruncatedOverrides). A legacy override lives in
	// another calendar than its master, and the hide writes that calendar. Its
	// access policy applies now, as it applies to the restore later.
	recIDs := make([]string, 0, len(hidden))
	rowIDs := make([]int64, 0, len(hidden))
	for _, o := range hidden {
		recIDs = append(recIDs, o.RecurrenceID)
		rowIDs = append(rowIDs, o.ID)
	}
	if err := ensureRowsWritableTx(ctx, qtx, master.CalendarID, rowIDs); err != nil {
		return err
	}
	hiddenList := strings.Join(recIDs, ",")
	hiddenIDs := formatRowIDs(rowIDs)

	if len(rowIDs) > 0 {
		if err := qtx.SoftDeleteEventsByIDs(ctx, rowIDs); err != nil {
			return fmt.Errorf("soft-delete future overrides: %w", err)
		}
	}

	if err := qtx.RecordEventTruncateDelete(ctx, storage.RecordEventTruncateDeleteParams{
		CalendarID:      master.CalendarID,
		Uid:             uid,
		CutoffTime:      cutoff,
		PreviousRrule:   prevRRule,
		HiddenOverrides: &hiddenList,
		RemovedRdates:   &removedRDates,
		HiddenIds:       &hiddenIDs,
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
func (s *Service) deleteFromInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (storage.Event, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Event{}, "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Read the master inside the transaction so the RRULE we truncate reflects
	// a concurrent writer's edits rather than a pre-transaction snapshot
	// (issue #116).
	master, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return storage.Event{}, "", wrapMasterLookup(err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return storage.Event{}, "", err
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
			return storage.Event{}, "", fmt.Errorf("update rrule: %w", err)
		}
	}

	if err := softDeleteOverridesAndRecordTruncation(ctx, qtx, master, instanceTime, prevRRule); err != nil {
		return storage.Event{}, "", err
	}

	// Read the master's updated_at back inside the transaction so the value we
	// return reflects exactly this truncation's write, with no chance of an
	// interleaved external edit in between.
	truncated, err := qtx.GetEvent(ctx, master.ID)
	if err != nil {
		return storage.Event{}, "", fmt.Errorf("read back master: %w", err)
	}
	postUpdated := truncated.UpdatedAt

	// Mark the master dirty — its RRULE and RDATEs were modified — inside
	// the same transaction so a failed mark rolls the truncation back rather
	// than committing a change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "event"); err != nil {
		return storage.Event{}, "", fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.Event{}, "", err
	}
	return master, postUpdated, nil
}

// DeleteSeries deletes a recurring master event and all its overrides.
// calendarID names the calendar of the series. Two calendars can hold the
// same UID (issue #842), so the rows of the other calendar stay. With 0,
// one live master is the only choice, and more give ErrAmbiguousUID. See
// findSeriesMasterTx.
func (s *Service) DeleteSeries(ctx context.Context, calendarID int64, uid string) error {
	_, err := s.deleteSeries(ctx, calendarID, uid)
	return err
}

// seriesDeletion is the record of one series delete.
type seriesDeletion struct {
	// Master is the master the delete read in the transaction. ID 0 means
	// no live master: the delete hid orphan rows.
	Master storage.Event
	// Label names the delete for the undo: the title of the master, or of
	// the first hidden row.
	Label string
	// CalendarID is the calendar of the delete.
	CalendarID int64
	// Hidden lists the orphan rows the delete hid, by ID. Empty for a
	// delete with a master: the series-delete log holds its rows.
	Hidden []int64
}

// deleteSeries is DeleteSeries. It returns the record of the delete.
func (s *Service) deleteSeries(ctx context.Context, calendarID int64, uid string) (seriesDeletion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return seriesDeletion{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Look up the master to get calendarID for tombstone creation. The
	// tombstone and the soft-delete commit together so a failed tombstone
	// write can't leave a tombstone for a still-live series whose next sync
	// would DELETE it from the server (issue #107).
	master, mErr := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	// Only ErrNoRows means "no master to track". A genuine lookup error must
	// abort. Otherwise the series is soft-deleted locally with no tombstone.
	// The next pull would then resurrect the series. This mirrors the guard
	// the todo and journal services already carry (issue #290).
	if mErr != nil && !errors.Is(mErr, sql.ErrNoRows) {
		return seriesDeletion{}, wrapMasterLookup(mErr)
	}
	if mErr == nil {
		calendarID = master.CalendarID
	} else if calendarID != 0 {
		// The named calendar holds no live series of the UID: its master is in the
		// trash, or it holds no row. When another calendar holds a live master, a
		// delete of the rows of this calendar would also take the legacy overrides
		// of that master. The delete then has no target. With no live master
		// anywhere, the rows are orphans (a purged master), and the delete takes
		// them.
		n, err := qtx.EventUIDMasterCalendars(ctx, uid)
		if err != nil {
			return seriesDeletion{}, fmt.Errorf("find master calendars: %w", err)
		}
		if n.Calendars > 0 {
			_, err := qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
				CalendarID: calendarID, Uid: uid,
			})
			found, err := storage.Found(err)
			if err != nil {
				return seriesDeletion{}, fmt.Errorf("get master: %w", err)
			}
			if found {
				return seriesDeletion{}, ErrSeriesInTrash
			}
			return seriesDeletion{}, wrapMasterLookup(mErr)
		}
	}

	// The access guard covers the calendars of the series only. A
	// read-only copy of the UID in another calendar does not block the
	// delete (issue #842).
	if err := ensureSeriesWritableIn(ctx, qtx, calendarID, uid); err != nil {
		return seriesDeletion{}, err
	}
	if mErr == nil {
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, master.CalendarID, uid); err != nil {
			return seriesDeletion{}, fmt.Errorf("create tombstone: %w", err)
		}
		// Record the live override rows that this delete hides, by ID,
		// so a restore of the series brings back exactly those rows.
		live, err := qtx.ListSeriesOverridesInCalendar(ctx, storage.ListSeriesOverridesInCalendarParams{
			CalendarID: master.CalendarID, Uid: uid,
		})
		if err != nil {
			return seriesDeletion{}, fmt.Errorf("list overrides: %w", err)
		}
		hidden := make([]int64, 0, len(live))
		for _, o := range live {
			hidden = append(hidden, o.ID)
		}
		if err := recordSeriesDeleteTx(ctx, qtx, master.CalendarID, uid, hidden, false); err != nil {
			return seriesDeletion{}, err
		}
	}

	if calendarID == 0 {
		// No calendar and no live master: the UID holds orphan rows
		// only. One calendar with live rows is the target, so the
		// delete logs the rows for the undo like a named calendar.
		calendars, err := qtx.ListLiveEventCalendarIDsByUID(ctx, uid)
		if err != nil {
			return seriesDeletion{}, fmt.Errorf("list calendars: %w", err)
		}
		switch len(calendars) {
		case 0:
			return seriesDeletion{}, ErrNotDeleted
		case 1:
			calendarID = calendars[0]
		default:
			return seriesDeletion{}, ErrAmbiguousUID
		}
	}
	if mErr != nil {
		// No live master anywhere: the live rows of the calendar are orphans. The
		// series rule assigns them to a master in the trash, if any. The delete
		// takes the rows of the calendar as they are, and logs them for the undo.
		// No row means nothing to delete, before any write.
		live, err := qtx.ListLiveOverridesInCalendarByUID(ctx, storage.ListLiveOverridesInCalendarByUIDParams{
			CalendarID: calendarID, Uid: uid,
		})
		if err != nil {
			return seriesDeletion{}, fmt.Errorf("list orphan rows: %w", err)
		}
		if len(live) == 0 {
			return seriesDeletion{}, ErrNotDeleted
		}
		// The log of an earlier series delete in this calendar can still name rows,
		// because the master of the calendar is in the trash. The new rows join
		// that list.
		ids := make([]int64, 0, len(live))
		for _, o := range live {
			ids = append(ids, o.ID)
		}
		if err := recordSeriesDeleteTx(ctx, qtx, calendarID, uid, ids, true); err != nil {
			return seriesDeletion{}, err
		}
		// The rows of a synced calendar have a server object. A tombstone
		// queues its delete, as for a series with a master, or the next
		// pull brings the rows back.
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, calendarID, uid); err != nil {
			return seriesDeletion{}, fmt.Errorf("create tombstone: %w", err)
		}
		if _, err := qtx.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{
			CalendarID: calendarID, Uid: uid,
		}); err != nil {
			return seriesDeletion{}, fmt.Errorf("soft-delete orphan rows: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return seriesDeletion{}, err
		}
		return seriesDeletion{Label: live[0].Title, CalendarID: calendarID, Hidden: ids}, nil
	}
	if err := qtx.SoftDeleteSeriesRowsInCalendar(ctx, storage.SoftDeleteSeriesRowsInCalendarParams{
		CalendarID: calendarID, Uid: uid,
	}); err != nil {
		return seriesDeletion{}, fmt.Errorf("soft-delete series: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return seriesDeletion{}, err
	}
	return seriesDeletion{Master: master, Label: master.Title, CalendarID: master.CalendarID}, nil
}
