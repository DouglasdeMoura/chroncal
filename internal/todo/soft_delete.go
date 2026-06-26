package todo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ErrNotDeleted is returned by Restore / Purge when the target row is not
// soft-deleted (either it never was, or it has already been restored, or
// it was purged). The CLI collapses this with ErrNotFound.
var ErrNotDeleted = errors.New("todo: row not soft-deleted (may have been purged)")

// RestoreByID un-hides a single soft-deleted todo. For an override it
// also strips the matching EXDATE from the master in the same
// transaction — otherwise the restored occurrence reappears as a row in
// the DB but stays hidden from expansion because the series still
// excludes that slot. Reconciles sync state so the next push re-CREATEs
// the resource server-side if the row was tombstoned.
func (s *Service) RestoreByID(ctx context.Context, id int64) error {
	r, err := s.q.GetTodoIncludingDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get todo: %w", err)
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		return ErrNotDeleted
	}

	// Standalone or master: just un-hide. No EXDATE to reverse.
	if r.RecurrenceID == "" {
		if err := s.q.RestoreTodo(ctx, id); err != nil {
			return fmt.Errorf("restore todo: %w", err)
		}
		return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
	}

	// Override: restore the row AND drop its EXDATE entry from the master
	// so expansion surfaces the occurrence again. Both changes must land
	// together or the row is visible-but-excluded.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.RestoreTodo(ctx, id); err != nil {
		return fmt.Errorf("restore todo: %w", err)
	}
	if err := clearMasterEXDATE(ctx, qtx, r.Uid, r.RecurrenceID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
}

// clearMasterEXDATE removes the EXDATE entry for recurrenceID from the
// master todo with uid, reversing the exclusion that the instance-delete
// path added. It only strips EXDATEs that a delete recorded in
// todo_exdate_deletes; EXDATEs that arrived via import (or a series delete,
// which never adds one) have no provenance row and survive restore —
// otherwise RestoreByUID would silently drop a legitimate imported EXDATE
// whose slot happens to match an override's recurrence_id (issue #86). A
// malformed recurrence_id is a data-integrity error and is propagated rather
// than swallowed. Must run inside the same transaction (qtx) that un-hides
// the override so the row is never visible-but-excluded.
func clearMasterEXDATE(ctx context.Context, qtx *storage.Queries, uid, recurrenceID string) error {
	log, err := qtx.GetTodoExdateDeleteByUIDRecurrence(ctx, storage.GetTodoExdateDeleteByUIDRecurrenceParams{
		Uid:          uid,
		RecurrenceID: recurrenceID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("get exdate log: %w", err)
	}

	master, err := qtx.GetTodoByUID(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Master gone; drop the now-orphaned provenance row.
			return qtx.DeleteTodoExdateDelete(ctx, log.ID)
		}
		return fmt.Errorf("get master: %w", err)
	}
	target, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return fmt.Errorf("parse recurrence_id %q: %w", recurrenceID, err)
	}
	existing := timeutil.ParseTimeList(storage.NullableToString(master.Exdates))
	filtered := removeTimeFromList(existing, target)
	if len(filtered) != len(existing) {
		if err := qtx.UpdateTodoExdates(ctx, storage.UpdateTodoExdatesParams{
			Exdates: storage.StringToNullable(timeutil.SerializeTimeList(filtered)),
			ID:      master.ID,
		}); err != nil {
			return fmt.Errorf("update exdates: %w", err)
		}
	}
	if err := qtx.DeleteTodoExdateDelete(ctx, log.ID); err != nil {
		return fmt.Errorf("delete exdate log: %w", err)
	}
	return nil
}

// removeTimeFromList returns list with the first element equal to target
// (after UTC normalization) removed. Used to reverse an EXDATE insertion
// when restoring a recurring override: a delete added exactly one EXDATE, so
// restore strips exactly one — any duplicate (e.g. a pre-existing imported
// exclusion at the same slot) is preserved. Mirrors event.removeTimeFromList.
func removeTimeFromList(list []time.Time, target time.Time) []time.Time {
	out := make([]time.Time, 0, len(list))
	targetKey := target.UTC().Format(time.RFC3339)
	removed := false
	for _, t := range list {
		if !removed && strings.EqualFold(t.UTC().Format(time.RFC3339), targetKey) {
			removed = true
			continue
		}
		out = append(out, t)
	}
	return out
}

// RestoreByUID un-hides every soft-deleted row with the given UID —
// master + overrides — and strips the matching EXDATE from the master for
// each restored override in the same transaction. Without the EXDATE
// cleanup the master would keep excluding those slots while also carrying
// the now-live overrides, which round-trips to iCal as a self-contradicting
// series (EXDATE + override for the same occurrence). Used by the CLI
// `todos restore <uid>` path. Mirrors event.RestoreByUID.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	master, err := s.q.GetTodoByUIDIncludingDeleted(ctx, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("get master: %w", err)
	}
	if err := s.restoreByUIDClearingExdates(ctx, uid); err != nil {
		return err
	}
	if err == nil {
		return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
	}
	return nil
}

// restoreByUIDClearingExdates un-hides every soft-deleted row for uid and
// clears the master EXDATE for each override that was soft-deleted, all in
// one transaction. The recurrence IDs are read before the restore because
// afterwards the rows are live and no longer match the deleted-overrides
// query.
func (s *Service) restoreByUIDClearingExdates(ctx context.Context, uid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	recurrenceIDs, err := qtx.ListDeletedTodoOverrideRecurrenceIDs(ctx, uid)
	if err != nil {
		return fmt.Errorf("list deleted override recurrence ids: %w", err)
	}
	if err := qtx.RestoreTodosByUID(ctx, uid); err != nil {
		return fmt.Errorf("restore by uid: %w", err)
	}
	for _, recurrenceID := range recurrenceIDs {
		if err := clearMasterEXDATE(ctx, qtx, uid, recurrenceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListDeleted returns soft-deleted todos for a calendar, newest-first.
func (s *Service) ListDeleted(ctx context.Context, calendarID int64) ([]Todo, error) {
	rows, err := s.q.ListDeletedTodosByCalendar(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

// GetIncludingDeleted returns a todo by ID even if it has been soft-
// deleted. Used by the trash view's detail popup.
func (s *Service) GetIncludingDeleted(ctx context.Context, id int64) (Todo, error) {
	r, err := s.q.GetTodoIncludingDeleted(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	return fromStorage(r), nil
}

// PurgeDeleted hard-deletes soft-deleted todos whose deleted_at predates
// olderThan. Children cascade via FK ON DELETE CASCADE.
func (s *Service) PurgeDeleted(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeSoftDeletedTodos(ctx, &cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeOldInstanceDeletes drops todo_exdate_deletes provenance rows older
// than olderThan. Returns the number of rows purged. The corresponding
// EXDATEs on the master stay in place — the user intended those instances to
// be gone. Mirrors event.PurgeOldInstanceDeletes.
func (s *Service) PurgeOldInstanceDeletes(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeOldTodoExdateDeletes(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeByID hard-deletes a single soft-deleted todo. Returns ErrNotDeleted
// when the row is live or absent so callers cannot accidentally purge a
// live todo by passing the wrong ID.
func (s *Service) PurgeByID(ctx context.Context, id int64) error {
	n, err := s.q.PurgeTodoByID(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotDeleted
	}
	return nil
}

// reconcileSyncAfterRestore clears any tombstone queued for this UID and
// marks the resource dirty so the next push re-CREATEs it server-side if
// the sync_resource was already swept out.
func (s *Service) reconcileSyncAfterRestore(ctx context.Context, calendarID int64, uid string) error {
	_ = s.q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{
		CalendarID: calendarID,
		Uid:        uid,
	})
	_ = storage.MarkResourceDirty(ctx, s.db, calendarID, uid, "todo")
	return nil
}
