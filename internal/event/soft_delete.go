package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/douglasdemoura/chroncal/internal/softdelete"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ErrNotDeleted is returned by Restore when the target row is not soft-deleted
// (either it never was, or it has already been restored, or it was purged
// from the database). The CLI collapses this with ErrNotFound.
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
// transient children); the heavy lifting is done by the per-Kind Restore
// method which finds the actual rows by UID.
type UndoMeta struct {
	Kind      UndoKind
	UID       string
	Label     string
	DeletedAt time.Time

	// UndoKindSingle only, when undoing DeleteInstanceWithUndo.
	RecurrenceID string

	// UndoKindFromInstance only.
	MasterRRuleBefore   string
	MasterUpdatedBefore time.Time
}

// DeleteWithUndo soft-deletes an event by ID and returns the metadata needed
// to reverse it. For an override, EXDATE mutation on the master is performed
// as part of the existing Delete flow. The returned UndoMeta covers the
// single-row un-hide.
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
		Kind:      UndoKindSingle,
		UID:       evt.UID,
		Label:     evt.Title,
		DeletedAt: time.Now().UTC(),
	}, nil
}

// DeleteInstanceWithUndo excludes an occurrence and returns undo metadata.
// The overridden instance (if any) is soft-deleted; on Restore we un-hide it
// and remove the EXDATE we added.
func (s *Service) DeleteInstanceWithUndo(ctx context.Context, uid string, instanceTime time.Time) (UndoMeta, error) {
	master, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return UndoMeta{}, fmt.Errorf("get master: %w", err)
	}
	label := master.Title
	if err := s.DeleteInstance(ctx, uid, instanceTime); err != nil {
		return UndoMeta{}, err
	}
	return UndoMeta{
		Kind:         UndoKindSingle,
		UID:          uid,
		Label:        label,
		DeletedAt:    time.Now().UTC(),
		RecurrenceID: instanceTime.UTC().Format(time.RFC3339),
	}, nil
}

// DeleteFromInstanceWithUndo truncates the series at instanceTime and returns
// the pre-truncation RRULE + master UpdatedAt so Restore can reverse the
// truncation exactly.
func (s *Service) DeleteFromInstanceWithUndo(ctx context.Context, uid string, instanceTime time.Time) (UndoMeta, error) {
	master, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return UndoMeta{}, fmt.Errorf("get master: %w", err)
	}
	prevRRule := storage.NullableToString(master.RecurrenceRule)

	// deleteFromInstance bumps the master's updated_at (UpdateEventRecurrenceRule
	// sets updated_at=now) and returns that post-truncation value, read back
	// inside the truncation transaction. The stale-master guard in
	// restoreFromInstance must expect this value, not the pre-truncation one —
	// otherwise the truncation's own write looks like a concurrent external edit
	// and Undo always fails. Capturing it in-tx (rather than via a separate read
	// after commit) also prevents a concurrent external edit landing between
	// commit and read from being mistaken for the baseline. Only edits that
	// advance updated_at *past* this point trip the guard.
	postUpdated, err := s.deleteFromInstance(ctx, uid, instanceTime)
	if err != nil {
		return UndoMeta{}, err
	}

	return UndoMeta{
		Kind:                UndoKindFromInstance,
		UID:                 uid,
		Label:               master.Title,
		DeletedAt:           time.Now().UTC(),
		MasterRRuleBefore:   prevRRule,
		MasterUpdatedBefore: parseStorageTime(postUpdated),
	}, nil
}

// DeleteSeriesWithUndo soft-deletes a master + all overrides and returns undo
// metadata. Restore calls RestoreByUID which un-hides every row with the UID.
func (s *Service) DeleteSeriesWithUndo(ctx context.Context, uid string) (UndoMeta, error) {
	master, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return UndoMeta{}, fmt.Errorf("get master: %w", err)
	}
	label := master.Title
	if err := s.DeleteSeries(ctx, uid); err != nil {
		return UndoMeta{}, err
	}
	return UndoMeta{
		Kind:      UndoKindSeries,
		UID:       uid,
		Label:     label,
		DeletedAt: time.Now().UTC(),
	}, nil
}

// RestoreUndo reverses a soft-delete operation recorded in UndoMeta. Dispatches
// by Kind. For FromInstance kinds, also rewrites the master's RRULE back to
// the pre-truncation value in the same transaction.
func (s *Service) RestoreUndo(ctx context.Context, meta UndoMeta) error {
	switch meta.Kind {
	case UndoKindSingle:
		return s.restoreSingle(ctx, meta.UID, meta.RecurrenceID)
	case UndoKindSeries:
		return s.restoreSeries(ctx, meta.UID)
	case UndoKindFromInstance:
		return s.restoreFromInstance(ctx, meta)
	default:
		return fmt.Errorf("unknown undo kind %d", meta.Kind)
	}
}

// RestoreByUID un-hides every soft-deleted row with the given UID (master and
// overrides). Used by the CLI `events restore <uid>` path.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	master, err := s.q.GetEventByUIDIncludingDeleted(ctx, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("get master: %w", err)
	}
	if err := s.restoreEventsByUIDClearingExdates(ctx, uid); err != nil {
		return err
	}
	if err == nil {
		return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
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

// GetIncludingDeleted returns a row by ID even if it's been soft-deleted.
// Used by the trash view's detail popup where the user wants to inspect
// what was deleted before restoring.
func (s *Service) GetIncludingDeleted(ctx context.Context, id int64) (Event, error) {
	r, err := s.q.GetEventIncludingDeleted(ctx, id)
	if err != nil {
		return Event{}, err
	}
	return FromStorage(r), nil
}

// PurgeDeleted hard-deletes rows soft-deleted before olderThan. Returns the
// number of rows purged. Children cascade via existing FK ON DELETE CASCADE.
func (s *Service) PurgeDeleted(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeSoftDeletedEvents(ctx, &cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeByID hard-deletes a single soft-deleted row. Returns ErrNotDeleted if
// the row is live (or absent) so callers can't accidentally purge a live event
// by passing the wrong ID.
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

// restoreSingle un-hides one row by (uid, recurrence_id=”); used for
// DeleteWithUndo and DeleteInstanceWithUndo single-row resurrection. For an
// override, callers should fall back to RestoreByUID since we don't know the
// recurrence_id. UndoKindSingle always targets the master UID, so this
// finds the master.
func (s *Service) restoreSingle(ctx context.Context, uid, recurrenceID string) error {
	r, err := s.q.GetEventByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Might be an override-only delete where the master UID has no
			// master row; fall back to UID-wide restore.
			return s.restoreEventsByUIDClearingExdates(ctx, uid)
		}
		return err
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		if recurrenceID != "" {
			if err := s.restoreEXDATEOnly(ctx, uid, recurrenceID, r.CalendarID); err != nil {
				return err
			}
			return nil
		}
		// Master is live; an override was probably the thing deleted.
		// Fall back to RestoreByUID which un-hides any deleted overrides
		// sharing this UID.
		return s.restoreEventsByUIDClearingExdates(ctx, uid)
	}
	if err := s.q.RestoreEvent(ctx, r.ID); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
}

func (s *Service) restoreSeries(ctx context.Context, uid string) error {
	// Find the master (including deleted) for sync reconciliation context.
	master, err := s.q.GetEventByUIDIncludingDeleted(ctx, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := s.restoreEventsByUIDClearingExdates(ctx, uid); err != nil {
		return err
	}
	if err == nil {
		return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
	}
	return nil
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
	if err := clearMasterEXDATE(ctx, qtx, r.Uid, r.RecurrenceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) restoreEXDATEOnly(ctx context.Context, uid, recurrenceID string, calendarID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// DeleteInstance soft-deletes the override at this recurrence_id (if one
	// existed) alongside adding the EXDATE; un-hide it so undo restores the
	// customized occurrence, not just the base instance. No-ops when the
	// deleted instance had no override.
	if err := qtx.RestoreEventByUIDAndRecurrenceID(ctx, storage.RestoreEventByUIDAndRecurrenceIDParams{
		Uid:          uid,
		RecurrenceID: recurrenceID,
	}); err != nil {
		return fmt.Errorf("restore override: %w", err)
	}
	if err := clearMasterEXDATE(ctx, qtx, uid, recurrenceID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, calendarID, uid)
}

func (s *Service) restoreEventsByUIDClearingExdates(ctx context.Context, uid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	recurrenceIDs, err := qtx.ListDeletedOverrideRecurrenceIDs(ctx, uid)
	if err != nil {
		return fmt.Errorf("list deleted override recurrence ids: %w", err)
	}
	if err := qtx.RestoreEventsByUID(ctx, uid); err != nil {
		return fmt.Errorf("restore by uid: %w", err)
	}
	for _, recurrenceID := range recurrenceIDs {
		if err := clearMasterEXDATE(ctx, qtx, uid, recurrenceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// clearMasterEXDATE reverses the EXDATE an instance-delete added for
// recurrenceID on the master event with uid. The provenance contract lives in
// softdelete.ClearMasterEXDATE; this wrapper only binds the event queries to
// the active transaction so the override is never visible-but-excluded.
func clearMasterEXDATE(ctx context.Context, qtx *storage.Queries, uid, recurrenceID string) error {
	return softdelete.ClearMasterEXDATE(ctx, softdelete.ExdateProvenance{
		GetDeleteLog: func(ctx context.Context) (int64, bool, error) {
			log, err := qtx.GetEventExdateDeleteByUIDRecurrence(ctx, storage.GetEventExdateDeleteByUIDRecurrenceParams{
				Uid:          uid,
				RecurrenceID: recurrenceID,
			})
			if errors.Is(err, sql.ErrNoRows) {
				return 0, false, nil
			}
			if err != nil {
				return 0, false, err
			}
			return log.ID, true, nil
		},
		GetMaster: func(ctx context.Context) (int64, string, bool, error) {
			master, err := qtx.GetEventByUID(ctx, uid)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, "", false, nil
			}
			if err != nil {
				return 0, "", false, err
			}
			return master.ID, storage.NullableToString(master.Exdates), true, nil
		},
		UpdateExdates: func(ctx context.Context, masterID int64, exdates string) error {
			return qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
				Exdates: storage.StringToNullable(exdates),
				ID:      masterID,
			})
		},
		DeleteDeleteLog: func(ctx context.Context, logID int64) error {
			return qtx.DeleteEventExdateDelete(ctx, logID)
		},
	}, recurrenceID)
}

func (s *Service) restoreFromInstance(ctx context.Context, meta UndoMeta) error {
	master, err := s.q.GetEventByUIDIncludingDeleted(ctx, meta.UID)
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}
	prevUpdated := parseStorageTime(master.UpdatedAt)
	if prevUpdated.After(meta.MasterUpdatedBefore.Add(time.Second)) {
		return fmt.Errorf("master advanced since delete (expected updated_at <= %s, got %s)",
			meta.MasterUpdatedBefore.Format(time.RFC3339), prevUpdated.Format(time.RFC3339))
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Rewrite RRULE back.
	if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
		RecurrenceRule: storage.StringToNullable(meta.MasterRRuleBefore),
		ID:             master.ID,
	}); err != nil {
		return fmt.Errorf("restore rrule: %w", err)
	}
	// Un-hide every soft-deleted row with this UID (the master was not
	// soft-deleted by DeleteFromInstance, only overrides were — but this is
	// idempotent).
	if err := qtx.RestoreEventsByUID(ctx, meta.UID); err != nil {
		return fmt.Errorf("restore overrides: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := storage.MarkResourceDirty(ctx, s.db, master.CalendarID, meta.UID, "event"); err != nil {
		return fmt.Errorf("mark resource dirty after restore: %w", err)
	}
	return nil
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
