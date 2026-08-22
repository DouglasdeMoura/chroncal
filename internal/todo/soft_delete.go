package todo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/softdelete"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ErrNotDeleted is returned by Restore / Purge when the target row is not
// soft-deleted. The row was never deleted, or it has already been restored,
// or it was purged. The CLI collapses this with ErrNotFound.
var ErrNotDeleted = errors.New("todo: row not soft-deleted (may have been purged)")

// RestoreByID un-hides a single soft-deleted todo. For an override it
// also strips the EXDATE that matches from the master in the same
// transaction. Otherwise the restored occurrence reappears as a row in
// the DB but stays hidden from expansion. The series still excludes that
// slot. The function reconciles sync state so the next push re-CREATEs
// the resource on the server if the row was tombstoned.
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
	if err := calendaraccess.EnsureWritable(ctx, s.q, r.CalendarID, todoComponent); err != nil {
		return err
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
	// together. Otherwise the row is visible-but-excluded.
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

// RestoreByUID un-hides every soft-deleted row with the given UID:
// master plus overrides. It strips the EXDATE that matches from the master
// for each restored override in the same transaction.
//
// Without the EXDATE cleanup the master would still exclude those slots
// while it also holds the now-live overrides. That round-trips to iCal as a
// contradictory series (EXDATE plus override for the same occurrence).
// Used by the CLI `todos restore <uid>` path. Mirrors event.RestoreByUID.
// Returns ErrNotDeleted when the UID matches no soft-deleted rows. Callers
// can then report "not found" instead of a false success.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	master, err := s.q.GetTodoByUIDIncludingDeleted(ctx, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("get master: %w", err)
	}
	if err := s.ensureSeriesWritable(ctx, uid); err != nil {
		return err
	}
	n, restoreErr := s.restoreByUIDClearingExdates(ctx, uid)
	if restoreErr != nil {
		return restoreErr
	}
	if n == 0 {
		return ErrNotDeleted
	}
	if err == nil {
		return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
	}
	return nil
}

// restoreByUIDClearingExdates un-hides every soft-deleted row for uid and
// clears the master EXDATE for each override that was soft-deleted, all in
// one transaction. It returns the number of rows un-hidden so callers can
// tell a real restore from a no-op (live or unknown UID). The recurrence
// IDs are read before the restore. After the restore the rows are live and
// no longer match the deleted-overrides query.
func (s *Service) restoreByUIDClearingExdates(ctx context.Context, uid string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	recurrenceIDs, err := qtx.ListDeletedTodoOverrideRecurrenceIDs(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("list deleted override recurrence ids: %w", err)
	}
	n, err := qtx.RestoreTodosByUID(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("restore by uid: %w", err)
	}
	for _, recurrenceID := range recurrenceIDs {
		if err := clearMasterEXDATE(ctx, qtx, uid, recurrenceID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
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
// than olderThan. Returns the number of rows purged. The related
// EXDATEs on the master stay in place. The user intended those instances to
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
// when the row is live or absent. Callers then cannot purge a live todo
// with the wrong ID.
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
// marks the resource dirty. The next push then re-CREATEs it on the server
// if the sync_resource was already swept out.
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

// ErrHasOverrides is returned when a delete targets a recurring master
// todo that has override instances. Use DeleteSeries instead.
var ErrHasOverrides = fmt.Errorf("todo has overrides: use DeleteSeries to delete the entire series")

// Delete soft-deletes a todo by ID. For a standalone todo it flips
// deleted_at. For an override it adds EXDATE to the master and
// soft-deletes the override in the same transaction so undo can reverse
// both sides. A recurring master with live overrides is rejected.
// Callers must use DeleteSeries.
func (s *Service) Delete(ctx context.Context, id int64) error {
	td, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if err := calendaraccess.EnsureWritable(ctx, s.q, td.CalendarID, todoComponent); err != nil {
		return err
	}
	// RDATE-only masters (no RRULE) are recurring too, so guard on either rule
	// or RDATEs; otherwise their overrides would be orphaned (#415).
	if (td.RecurrenceRule != "" || td.RDates != "") && td.RecurrenceID == "" {
		overrides, err := s.q.ListTodoOverridesByUID(ctx, td.UID)
		if err != nil {
			return fmt.Errorf("check overrides: %w", err)
		}
		if len(overrides) > 0 {
			return ErrHasOverrides
		}
	}

	if td.RecurrenceID == "" {
		// Tombstone and soft-delete commit together. A failed tombstone write
		// cannot leave a soft-deleted row whose next sync DELETEs a still-live
		// server resource (issue #107).
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, td.CalendarID, td.UID); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
		if err := qtx.SoftDeleteTodo(ctx, id); err != nil {
			return fmt.Errorf("soft-delete todo: %w", err)
		}
		if err := storage.MarkResourceDirty(ctx, tx, td.CalendarID, td.UID, "todo"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	if td.RecurrenceID != "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)

		master, err := qtx.GetTodoByUID(ctx, td.UID)
		// A genuine lookup error (for example SQLITE_BUSY) must not collapse
		// into the "no master" path. That path would soft-delete the override
		// and skip its EXDATE/provenance records. Series expansion would then
		// restore the occurrence (issue #290). Only a missing master (ErrNoRows)
		// may skip those records.
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get master: %w", err)
		}
		if err == nil {
			existing := timeutil.ParseTimeList(storage.NullableToString(master.Exdates))
			recIDTime, parseErr := timeutil.ParseRecurrenceID(td.RecurrenceID)
			if parseErr != nil {
				// A malformed recurrence_id cannot be excluded from the
				// master. A soft-delete of the override would then restore the
				// occurrence via series expansion. Return an error. The restore
				// path treats the same parse failure as fatal.
				return fmt.Errorf("parse recurrence_id %q: %w", td.RecurrenceID, parseErr)
			}
			existing = append(existing, recIDTime)
			if err := qtx.UpdateTodoExdates(ctx, storage.UpdateTodoExdatesParams{
				Exdates: storage.StringToNullable(timeutil.SerializeTimeList(existing)),
				ID:      master.ID,
			}); err != nil {
				return fmt.Errorf("update exdates: %w", err)
			}
			// Record provenance so restore knows this EXDATE was
			// delete-added (and may be stripped) rather than imported.
			if err := qtx.RecordTodoExdateDelete(ctx, storage.RecordTodoExdateDeleteParams{
				CalendarID:   master.CalendarID,
				Uid:          td.UID,
				RecurrenceID: td.RecurrenceID,
			}); err != nil {
				return fmt.Errorf("record exdate delete: %w", err)
			}
		}

		if err := qtx.SoftDeleteTodo(ctx, id); err != nil {
			return fmt.Errorf("soft-delete todo: %w", err)
		}
		// Mark the master dirty — its EXDATE was modified — inside the same
		// transaction. A failed mark then rolls the EXDATE change back. The
		// path does not commit a change that is never pushed (issue #107).
		if err := storage.MarkResourceDirty(ctx, tx, td.CalendarID, td.UID, "todo"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	// Unreachable: RecurrenceID is either "" (handled above) or non-empty.
	if err := s.q.SoftDeleteTodo(ctx, id); err != nil {
		return err
	}
	_ = storage.MarkResourceDirty(ctx, s.db, td.CalendarID, td.UID, "todo")
	return nil
}

// DeleteSeries soft-deletes a recurring master todo and every override
// with its UID. A tombstone is created when the master is synced so
// the next push sends DELETE to the server. The local rows stay in
// place until purge so the user can restore them.
func (s *Service) DeleteSeries(ctx context.Context, uid string) error {
	// Resolve every calendar the UID spans before a transaction opens. A
	// read-only or VEVENT-only collection then rejects the whole series
	// delete up front. Distinct calendar IDs across all rows (not just the
	// master) cover orphaned overrides and series-tail rows even after the
	// master has been purged on its own.
	if err := s.ensureSeriesWritable(ctx, uid); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Tombstone, dirty-mark, and soft-delete commit together. A failed
	// sync-track write cannot leave a tombstone for a still-live series
	// whose next sync would DELETE it from the server (issue #107). No
	// master means there is nothing to track.
	master, mErr := qtx.GetTodoByUID(ctx, uid)
	// Only ErrNoRows means "no master to track". A genuine lookup error must
	// abort. Otherwise the series is soft-deleted locally without a tombstone
	// or dirty mark. It would then resurface on the next pull (issue #290).
	if mErr != nil && !errors.Is(mErr, sql.ErrNoRows) {
		return fmt.Errorf("get master: %w", mErr)
	}
	haveMaster := mErr == nil
	if haveMaster {
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, master.CalendarID, uid); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
	}

	if err := qtx.SoftDeleteTodosByUID(ctx, uid); err != nil {
		return fmt.Errorf("soft-delete series: %w", err)
	}
	if haveMaster {
		if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "todo"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
	}
	return tx.Commit()
}
