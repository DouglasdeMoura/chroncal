package todo

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

// clearMasterEXDATE reverses the EXDATE an instance-delete added for
// recurrenceID on the master todo with uid. The provenance contract lives in
// softdelete.ClearMasterEXDATE; this wrapper only binds the todo queries to
// the active transaction so the override is never visible-but-excluded.
func clearMasterEXDATE(ctx context.Context, qtx *storage.Queries, uid, recurrenceID string) error {
	return softdelete.ClearMasterEXDATE(ctx, softdelete.ExdateProvenance{
		GetDeleteLog: func(ctx context.Context) (int64, bool, error) {
			log, err := qtx.GetTodoExdateDeleteByUIDRecurrence(ctx, storage.GetTodoExdateDeleteByUIDRecurrenceParams{
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
			master, err := qtx.GetTodoByUID(ctx, uid)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, "", false, nil
			}
			if err != nil {
				return 0, "", false, err
			}
			return master.ID, storage.NullableToString(master.Exdates), true, nil
		},
		UpdateExdates: func(ctx context.Context, masterID int64, exdates string) error {
			return qtx.UpdateTodoExdates(ctx, storage.UpdateTodoExdatesParams{
				Exdates: storage.StringToNullable(exdates),
				ID:      masterID,
			})
		},
		DeleteDeleteLog: func(ctx context.Context, logID int64) error {
			return qtx.DeleteTodoExdateDelete(ctx, logID)
		},
	}, recurrenceID)
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
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
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
	if err := s.q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{
		CalendarID: calendarID,
		Uid:        uid,
	}); err != nil {
		return fmt.Errorf("clear tombstone after restore: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, calendarID, uid, "todo"); err != nil {
		return fmt.Errorf("mark resource dirty after restore: %w", err)
	}
	return nil
}
