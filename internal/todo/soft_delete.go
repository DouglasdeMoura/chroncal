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

	master, err := qtx.GetTodoByUID(ctx, r.Uid)
	if err == nil {
		existing := timeutil.ParseTimeList(storage.NullableToString(master.Exdates))
		target, parseErr := timeutil.ParseRecurrenceID(r.RecurrenceID)
		if parseErr == nil {
			filtered := removeTimeFromList(existing, target)
			if len(filtered) != len(existing) {
				if err := qtx.UpdateTodoExdates(ctx, storage.UpdateTodoExdatesParams{
					Exdates: storage.StringToNullable(timeutil.SerializeTimeList(filtered)),
					ID:      master.ID,
				}); err != nil {
					return fmt.Errorf("update exdates: %w", err)
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
}

// removeTimeFromList returns list with every element equal to target
// (after UTC normalization) removed. Used to reverse an EXDATE insertion
// when restoring a recurring override.
func removeTimeFromList(list []time.Time, target time.Time) []time.Time {
	out := make([]time.Time, 0, len(list))
	targetKey := target.UTC().Format(time.RFC3339)
	for _, t := range list {
		if strings.EqualFold(t.UTC().Format(time.RFC3339), targetKey) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// RestoreByUID un-hides every soft-deleted row with the given UID —
// master + overrides. Used by the CLI `todos restore <uid>` path.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	master, err := s.q.GetTodoByUIDIncludingDeleted(ctx, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("get master: %w", err)
	}
	if err := s.q.RestoreTodosByUID(ctx, uid); err != nil {
		return fmt.Errorf("restore by uid: %w", err)
	}
	if err == nil {
		return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
	}
	return nil
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
	cutoff := olderThan.UTC().Format(storageTimeFormat)
	n, err := s.q.PurgeSoftDeletedTodos(ctx, &cutoff)
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

const storageTimeFormat = "2006-01-02T15:04:05Z"
