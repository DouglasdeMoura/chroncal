package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if err := clearMasterEXDATE(ctx, qtx, r.Uid, r.RecurrenceID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reconcileSyncAfterRestore(ctx, r.CalendarID, r.Uid)
}

// clearMasterEXDATE removes the EXDATE entry for recurrenceID from the
// master journal with uid, reversing the exclusion that the instance-delete
// path added. It only strips EXDATEs that a delete recorded in
// journal_exdate_deletes; EXDATEs that arrived via import (or a series
// delete, which never adds one) have no provenance row and survive restore —
// otherwise RestoreByUID would silently drop a legitimate imported EXDATE
// whose slot happens to match an override's recurrence_id (issue #86). A
// malformed recurrence_id is a data-integrity error and is propagated rather
// than swallowed. Must run inside the same transaction (qtx) that un-hides
// the override so the row is never visible-but-excluded.
func clearMasterEXDATE(ctx context.Context, qtx *storage.Queries, uid, recurrenceID string) error {
	log, err := qtx.GetJournalExdateDeleteByUIDRecurrence(ctx, storage.GetJournalExdateDeleteByUIDRecurrenceParams{
		Uid:          uid,
		RecurrenceID: recurrenceID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("get exdate log: %w", err)
	}

	master, err := qtx.GetJournalByUID(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Master gone; drop the now-orphaned provenance row.
			return qtx.DeleteJournalExdateDelete(ctx, log.ID)
		}
		return fmt.Errorf("get master: %w", err)
	}
	target, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return fmt.Errorf("parse recurrence_id %q: %w", recurrenceID, err)
	}
	existing := timeutil.ParseTimeList(storage.NullableToString(master.Exdates))
	filtered := timeutil.RemoveTimeFromList(existing, target)
	if len(filtered) != len(existing) {
		if err := qtx.UpdateJournalExdates(ctx, storage.UpdateJournalExdatesParams{
			Exdates: storage.StringToNullable(timeutil.SerializeTimeList(filtered)),
			ID:      master.ID,
		}); err != nil {
			return fmt.Errorf("update exdates: %w", err)
		}
	}
	if err := qtx.DeleteJournalExdateDelete(ctx, log.ID); err != nil {
		return fmt.Errorf("delete exdate log: %w", err)
	}
	return nil
}

// RestoreByUID un-hides every soft-deleted row sharing uid — master plus
// overrides — and strips the matching EXDATE from the master for each
// restored override in the same transaction. Without the EXDATE cleanup the
// master would keep excluding those slots while also carrying the now-live
// overrides, which round-trips to iCal as a self-contradicting series
// (EXDATE + override for the same occurrence). Used by the CLI
// `journals restore <uid>` path. Mirrors event.RestoreByUID.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	master, err := s.q.GetJournalByUIDIncludingDeleted(ctx, uid)
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

	recurrenceIDs, err := qtx.ListDeletedJournalOverrideRecurrenceIDs(ctx, uid)
	if err != nil {
		return fmt.Errorf("list deleted override recurrence ids: %w", err)
	}
	if err := qtx.RestoreJournalsByUID(ctx, uid); err != nil {
		return fmt.Errorf("restore by uid: %w", err)
	}
	for _, recurrenceID := range recurrenceIDs {
		if err := clearMasterEXDATE(ctx, qtx, uid, recurrenceID); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeSoftDeletedJournals(ctx, &cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeOldInstanceDeletes drops journal_exdate_deletes provenance rows older
// than olderThan. Returns the number of rows purged. The corresponding
// EXDATEs on the master stay in place — the user intended those instances to
// be gone. Mirrors event.PurgeOldInstanceDeletes.
func (s *Service) PurgeOldInstanceDeletes(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeOldJournalExdateDeletes(ctx, cutoff)
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
	if err := s.q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{
		CalendarID: calendarID,
		Uid:        uid,
	}); err != nil {
		return fmt.Errorf("clear tombstone after restore: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, calendarID, uid, "journal"); err != nil {
		return fmt.Errorf("mark resource dirty after restore: %w", err)
	}
	return nil
}
