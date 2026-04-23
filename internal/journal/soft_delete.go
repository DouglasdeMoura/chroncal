package journal

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
// soft-deleted. The CLI collapses this with ErrNotFound.
var ErrNotDeleted = errors.New("journal: row not soft-deleted (may have been purged)")

// RestoreByID un-hides a single soft-deleted journal. For an override it
// also strips the matching EXDATE from the master in the same
// transaction — otherwise the restored occurrence reappears as a row in
// the DB but stays hidden from expansion because the series still
// excludes that slot.
func (s *Service) RestoreByID(ctx context.Context, id int64) error {
	r, err := s.q.GetJournalIncludingDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get journal: %w", err)
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		return ErrNotDeleted
	}

	if r.RecurrenceID == "" {
		if err := s.q.RestoreJournal(ctx, id); err != nil {
			return fmt.Errorf("restore journal: %w", err)
		}
		return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.RestoreJournal(ctx, id); err != nil {
		return fmt.Errorf("restore journal: %w", err)
	}

	master, err := qtx.GetJournalByUID(ctx, r.Uid)
	if err == nil {
		existing := timeutil.ParseTimeList(storage.NullableToString(master.Exdates))
		target, parseErr := timeutil.ParseRecurrenceID(r.RecurrenceID)
		if parseErr == nil {
			filtered := removeTimeFromList(existing, target)
			if len(filtered) != len(existing) {
				if err := qtx.UpdateJournalExdates(ctx, storage.UpdateJournalExdatesParams{
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

// RestoreByUID un-hides every soft-deleted row sharing uid — master plus
// overrides. Used by the CLI `journals restore <uid>` path.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	master, err := s.q.GetJournalByUIDIncludingDeleted(ctx, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("get master: %w", err)
	}
	if err := s.q.RestoreJournalsByUID(ctx, uid); err != nil {
		return fmt.Errorf("restore by uid: %w", err)
	}
	if err == nil {
		return s.reconcileSyncAfterRestore(ctx, master.CalendarID, uid)
	}
	return nil
}

// ListDeleted returns soft-deleted journals for a calendar, newest-first.
func (s *Service) ListDeleted(ctx context.Context, calendarID int64) ([]Journal, error) {
	rows, err := s.q.ListDeletedJournalsByCalendar(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

// GetIncludingDeleted returns a journal by ID even if it has been soft-
// deleted. Used by the trash view's detail popup.
func (s *Service) GetIncludingDeleted(ctx context.Context, id int64) (Journal, error) {
	r, err := s.q.GetJournalIncludingDeleted(ctx, id)
	if err != nil {
		return Journal{}, err
	}
	return fromStorage(r), nil
}

// PurgeDeleted hard-deletes soft-deleted journals whose deleted_at
// predates olderThan. Children cascade via FK ON DELETE CASCADE.
func (s *Service) PurgeDeleted(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(storageTimeFormat)
	n, err := s.q.PurgeSoftDeletedJournals(ctx, &cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeByID hard-deletes a single soft-deleted journal. Returns
// ErrNotDeleted when the row is live or absent so callers cannot
// accidentally purge a live entry.
func (s *Service) PurgeByID(ctx context.Context, id int64) error {
	n, err := s.q.PurgeJournalByID(ctx, id)
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
	_ = storage.MarkResourceDirty(ctx, s.db, calendarID, uid, "journal")
	return nil
}

const storageTimeFormat = "2006-01-02T15:04:05Z"
