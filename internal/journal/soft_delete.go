package journal

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
// soft-deleted. The CLI collapses this with ErrNotFound.
var ErrNotDeleted = errors.New("journal: row not soft-deleted (may have been purged)")

// RestoreByID un-hides a single soft-deleted journal. For an override it
// also strips the EXDATE that matches from the master in the same
// transaction. Otherwise the restored occurrence reappears as a row in
// the DB but stays hidden from expansion. The series still excludes that
// slot.
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
	if err := s.ensureWritable(ctx, r.CalendarID); err != nil {
		return err
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

// clearMasterEXDATE reverses the EXDATE an instance-delete added for
// recurrenceID on the master journal with uid. The provenance contract lives
// in softdelete.ClearMasterEXDATE; this wrapper only binds the journal queries
// to the active transaction so the override is never visible-but-excluded.
func clearMasterEXDATE(ctx context.Context, qtx *storage.Queries, uid, recurrenceID string) error {
	return softdelete.ClearMasterEXDATE(ctx, softdelete.ExdateProvenance{
		GetDeleteLog: func(ctx context.Context) (int64, bool, error) {
			log, err := qtx.GetJournalExdateDeleteByUIDRecurrence(ctx, storage.GetJournalExdateDeleteByUIDRecurrenceParams{
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
			master, err := qtx.GetJournalByUID(ctx, uid)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, "", false, nil
			}
			if err != nil {
				return 0, "", false, err
			}
			return master.ID, storage.NullableToString(master.Exdates), true, nil
		},
		UpdateExdates: func(ctx context.Context, masterID int64, exdates string) error {
			return qtx.UpdateJournalExdates(ctx, storage.UpdateJournalExdatesParams{
				Exdates: storage.StringToNullable(exdates),
				ID:      masterID,
			})
		},
		DeleteDeleteLog: func(ctx context.Context, logID int64) error {
			return qtx.DeleteJournalExdateDelete(ctx, logID)
		},
	}, recurrenceID)
}

// RestoreByUID un-hides every soft-deleted row with uid: master plus
// overrides. It strips the EXDATE that matches from the master for each
// restored override in the same transaction.
//
// Without the EXDATE cleanup the master would still exclude those slots
// while it also holds the now-live overrides. That round-trips to iCal as a
// contradictory series (EXDATE plus override for the same occurrence).
// Used by the CLI `journals restore <uid>` path. Mirrors event.RestoreByUID.
// Returns ErrNotDeleted when the UID matches no soft-deleted rows. Callers
// can then report "not found" instead of a false success.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	// Resolve every calendar that owns a row with this UID before restore.
	// The master may have been purged, and only orphaned override or
	// series-tail rows remain. The per-UID master lookup (recurrence_id = "")
	// would miss them.
	calIDs, gErr := s.calendarIDsForUID(ctx, uid)
	if gErr != nil {
		return gErr
	}
	if err := s.ensureAllWritable(ctx, calIDs); err != nil {
		return err
	}

	master, err := s.q.GetJournalByUIDIncludingDeleted(ctx, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("get master: %w", err)
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

	recurrenceIDs, err := qtx.ListDeletedJournalOverrideRecurrenceIDs(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("list deleted override recurrence ids: %w", err)
	}
	n, err := qtx.RestoreJournalsByUID(ctx, uid)
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

// ListDeleted returns soft-deleted journals for a calendar, newest-first.
func (s *Service) ListDeleted(ctx context.Context, calendarID int64) ([]Journal, error) {
	rows, err := s.q.ListDeletedJournalsByCalendar(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
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
// than olderThan. Returns the number of rows purged. The related
// EXDATEs on the master stay in place. The user intended those instances to
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
// marks the resource dirty. The next push then re-CREATEs it on the server
// if the sync_resource was already swept out.
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

// ErrHasOverrides is returned when a delete targets a recurring master
// journal that has override instances. Use DeleteSeries instead.
var ErrHasOverrides = fmt.Errorf("journal has overrides: use DeleteSeries to delete the entire series")

// Delete soft-deletes a journal by ID. For a standalone journal it flips
// deleted_at. For an override it adds EXDATE to the master and
// soft-deletes the override in the same transaction. A recurring master with
// live overrides is rejected. Callers must use DeleteSeries.
func (s *Service) Delete(ctx context.Context, id int64) error {
	j, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.ensureWritable(ctx, j.CalendarID); err != nil {
		return err
	}

	// If this is a recurring master, check for overrides. RDATE-only masters
	// (no RRULE) are recurring too, so guard on either rule or RDATEs (#471).
	if (j.RecurrenceRule != "" || j.RDates != "") && j.RecurrenceID == "" {
		overrides, err := s.q.ListJournalOverridesByUID(ctx, j.UID)
		if err != nil {
			return fmt.Errorf("check overrides: %w", err)
		}
		if len(overrides) > 0 {
			return ErrHasOverrides
		}
	}

	if j.RecurrenceID == "" {
		// Tombstone and soft-delete commit together. A failed tombstone write
		// cannot leave a soft-deleted row whose next sync DELETEs a still-live
		// server resource (issue #107).
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, j.CalendarID, j.UID); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
		if err := qtx.SoftDeleteJournal(ctx, id); err != nil {
			return fmt.Errorf("soft-delete journal: %w", err)
		}
		if err := storage.MarkResourceDirty(ctx, tx, j.CalendarID, j.UID, "journal"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	if j.RecurrenceID != "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)

		master, err := qtx.GetJournalByUID(ctx, j.UID)
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
			recIDTime, parseErr := timeutil.ParseRecurrenceID(j.RecurrenceID)
			if parseErr != nil {
				// A malformed recurrence_id cannot be excluded from the
				// master. A soft-delete of the override would then restore the
				// occurrence via series expansion. Return an error. The restore
				// path treats the same parse failure as fatal.
				return fmt.Errorf("parse recurrence_id %q: %w", j.RecurrenceID, parseErr)
			}
			existing = append(existing, recIDTime)
			if err := qtx.UpdateJournalExdates(ctx, storage.UpdateJournalExdatesParams{
				Exdates: storage.StringToNullable(timeutil.SerializeTimeList(existing)),
				ID:      master.ID,
			}); err != nil {
				return fmt.Errorf("update exdates: %w", err)
			}
			// Record provenance so restore knows this EXDATE was
			// delete-added (and may be stripped) rather than imported.
			if err := qtx.RecordJournalExdateDelete(ctx, storage.RecordJournalExdateDeleteParams{
				CalendarID:   master.CalendarID,
				Uid:          j.UID,
				RecurrenceID: j.RecurrenceID,
			}); err != nil {
				return fmt.Errorf("record exdate delete: %w", err)
			}
		}

		if err := qtx.SoftDeleteJournal(ctx, id); err != nil {
			return fmt.Errorf("soft-delete journal: %w", err)
		}
		// Mark the master dirty — its EXDATE was modified — inside the same
		// transaction. A failed mark then rolls the EXDATE change back. The
		// path does not commit a change that is never pushed (issue #107).
		if err := storage.MarkResourceDirty(ctx, tx, j.CalendarID, j.UID, "journal"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	// Unreachable: RecurrenceID is either "" (handled above) or non-empty.
	if err := s.q.SoftDeleteJournal(ctx, id); err != nil {
		return err
	}
	_ = storage.MarkResourceDirty(ctx, s.db, j.CalendarID, j.UID, "journal")
	return nil
}

// DeleteSeries soft-deletes a recurring master journal and every override
// with its UID. A tombstone is queued when the master is synced so the
// next push sends DELETE to the server. The local rows stay in place
// until purge so the user can restore them.
func (s *Service) DeleteSeries(ctx context.Context, uid string) error {
	// Resolve every calendar that owns a row with this UID: master,
	// override, or series-tail. A read-only or VJOURNAL-unsupported
	// collection is then refused even when the master has been purged and
	// only orphaned overrides remain. The transaction body re-resolves the
	// master for tombstone records.
	calIDs, err := s.calendarIDsForUID(ctx, uid)
	if err != nil {
		return err
	}
	if err := s.ensureAllWritable(ctx, calIDs); err != nil {
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
	master, mErr := qtx.GetJournalByUID(ctx, uid)
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

	if err := qtx.SoftDeleteJournalsByUID(ctx, uid); err != nil {
		return fmt.Errorf("soft-delete series: %w", err)
	}
	if haveMaster {
		if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "journal"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
	}
	return tx.Commit()
}
