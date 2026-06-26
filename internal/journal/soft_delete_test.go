package journal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestDeleteSeries_DirtyMarkGatedOnMaster verifies that DeleteSeries on a
// synced calendar marks the series resource dirty when the master exists,
// and does NOT touch sync_resources (in particular no spurious calendar_id=0
// row) when no master row exists for the UID. Regression test for the
// master-existence guard being defeated by err reuse (issue #119).
func TestDeleteSeries_DirtyMarkGatedOnMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	testutil.LinkCalendarToAccount(t, db)

	// Master exists -> series should be marked dirty.
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "have-master", CalendarID: 1, Summary: "Weekly",
		StartDate:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	if err := svc.DeleteSeries(ctx, master.UID); err != nil {
		t.Fatalf("DeleteSeries (master): %v", err)
	}
	var dirty int
	if err := db.QueryRowContext(ctx,
		`SELECT dirty FROM sync_resources WHERE calendar_id = 1 AND uid = ?`, master.UID,
	).Scan(&dirty); err != nil {
		t.Fatalf("expected sync_resources row for master series: %v", err)
	}
	if dirty != 1 {
		t.Errorf("master series dirty = %d, want 1", dirty)
	}

	// No master row for this UID -> must not write any sync_resources row,
	// and in particular nothing keyed on calendar_id 0.
	if err := svc.DeleteSeries(ctx, "ghost-uid"); err != nil {
		t.Fatalf("DeleteSeries (ghost): %v", err)
	}
	var ghost int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_resources WHERE uid = 'ghost-uid' OR calendar_id = 0`,
	).Scan(&ghost); err != nil {
		t.Fatalf("count sync_resources: %v", err)
	}
	if ghost != 0 {
		t.Errorf("spurious sync_resources rows for missing master = %d, want 0", ghost)
	}
}

// TestSoftDelete_Standalone verifies:
//   - Delete sets deleted_at, row stays in DB
//   - Get fails (filtered)
//   - ListDeleted surfaces it
//   - RestoreByID un-hides it
func TestSoftDelete_Standalone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)

	if err := svc.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := svc.Get(ctx, j.ID); err == nil {
		t.Fatal("Get should fail after soft-delete")
	}

	deleted, err := svc.ListDeleted(ctx, j.CalendarID)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 1 || deleted[0].ID != j.ID {
		t.Fatalf("ListDeleted = %+v, want one row with id %d", deleted, j.ID)
	}

	if err := svc.RestoreByID(ctx, j.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if _, err := svc.Get(ctx, j.ID); err != nil {
		t.Fatalf("Get after restore: %v", err)
	}
}

// TestSoftDelete_Series verifies DeleteSeries soft-deletes master + overrides
// and RestoreByUID un-hides them all.
func TestSoftDelete_Series(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "daily-uid", CalendarID: 1, Summary: "Daily Journal",
		StartDate:      "2026-04-01",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	_, err = svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Summary: "Daily Journal (amended)",
		StartDate:    "2026-04-03",
		RecurrenceID: time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	if err := svc.DeleteSeries(ctx, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}

	deleted, err := svc.ListDeleted(ctx, 1)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("ListDeleted = %d, want 2 (master + override)", len(deleted))
	}

	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if _, err := svc.Get(ctx, master.ID); err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
}

// TestSoftDelete_ListExcludesDeleted covers the live read path.
func TestSoftDelete_ListExcludesDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)
	if err := svc.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	rows, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		if r.ID == j.ID {
			t.Fatalf("List returned soft-deleted row %d", j.ID)
		}
	}
}

// TestSoftDelete_RestoreByID_ErrNotDeleted matches the event/todo contract.
func TestSoftDelete_RestoreByID_ErrNotDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)

	if err := svc.RestoreByID(ctx, j.ID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreByID on live row err = %v, want ErrNotDeleted", err)
	}
	if err := svc.RestoreByID(ctx, 999_999); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreByID on missing row err = %v, want ErrNotDeleted", err)
	}
}

// TestSoftDelete_PurgeDeleted drops rows past the retention window.
func TestSoftDelete_PurgeDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)
	if err := svc.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	future := time.Now().Add(time.Hour)
	n, err := svc.PurgeDeleted(ctx, future)
	if err != nil {
		t.Fatalf("PurgeDeleted: %v", err)
	}
	if n != 1 {
		t.Fatalf("PurgeDeleted returned %d, want 1", n)
	}
}

// TestSoftDelete_PurgeByID_RefusesLiveRow verifies PurgeByID only drops
// soft-deleted rows and returns ErrNotDeleted for live or missing rows.
func TestSoftDelete_PurgeByID_RefusesLiveRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)

	if err := svc.PurgeByID(ctx, j.ID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("PurgeByID on live row err = %v, want ErrNotDeleted", err)
	}
	if _, err := svc.Get(ctx, j.ID); err != nil {
		t.Fatalf("live row should still be readable: %v", err)
	}
	if err := svc.PurgeByID(ctx, 999_999); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("PurgeByID on missing row err = %v, want ErrNotDeleted", err)
	}

	if err := svc.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.PurgeByID(ctx, j.ID); err != nil {
		t.Fatalf("PurgeByID on soft-deleted row: %v", err)
	}
	deleted, err := svc.ListDeleted(ctx, j.CalendarID)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("ListDeleted after PurgeByID = %d, want 0", len(deleted))
	}
}

// TestSoftDelete_RestoreOverrideClearsExdate verifies that restoring a
// recurring override also strips the matching EXDATE from the master.
func TestSoftDelete_RestoreOverrideClearsExdate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "daily-uid", CalendarID: 1, Summary: "Daily Journal",
		StartDate:      "2026-04-01",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Summary: "Daily Journal (amended)",
		StartDate:    "2026-04-03",
		RecurrenceID: time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete override: %v", err)
	}

	afterDelete, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master after delete: %v", err)
	}
	if afterDelete.ExDates == "" {
		t.Fatal("master.ExDates empty after override delete — EXDATE should have been added")
	}

	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID override: %v", err)
	}

	afterRestore, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master after restore: %v", err)
	}
	if afterRestore.ExDates != "" {
		t.Fatalf("master.ExDates = %q, want empty after override restore", afterRestore.ExDates)
	}
}

// TestSoftDelete_MalformedRecurrenceID is the regression test for
// issue #120: deleting an override whose recurrence_id cannot be parsed
// must fail loudly rather than soft-delete the row while silently
// skipping the EXDATE addition. Otherwise the override is hidden but the
// master keeps expanding the occurrence, resurrecting the "deleted" slot.
// The restore path already propagates this parse error; delete must too.
func TestSoftDelete_MalformedRecurrenceID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "daily-uid", CalendarID: 1, Summary: "Daily Journal",
		StartDate:      "2026-04-01",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Summary: "Daily Journal (amended)",
		StartDate:    "2026-04-03",
		RecurrenceID: time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Simulate corrupt/imported data: a non-parseable recurrence_id.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE journals SET recurrence_id = ? WHERE id = ?", "not-a-date", override.ID); err != nil {
		t.Fatalf("corrupt recurrence_id: %v", err)
	}

	// Delete must fail rather than silently skip the EXDATE addition.
	if err := svc.Delete(ctx, override.ID); err == nil {
		t.Fatal("Delete with malformed recurrence_id should fail, got nil")
	}

	// The override must remain live (transaction rolled back), so the
	// occurrence is still represented by the override and not resurrected.
	if _, err := svc.Get(ctx, override.ID); err != nil {
		t.Fatalf("override should still be live after failed delete: %v", err)
	}
}

// TestSoftDelete_RestoreByUIDClearsExdate is the regression test for
// issue #72: restoring a recurring series by UID must strip the EXDATEs
// the instance-delete path added to the master, just like RestoreByID
// does for a single override. Otherwise the master keeps excluding the
// slot while also carrying the now-live override, which exports to iCal
// as a self-contradicting series.
func TestSoftDelete_RestoreByUIDClearsExdate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "daily-uid", CalendarID: 1, Summary: "Daily Journal",
		StartDate:      "2026-04-01",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Summary: "Daily Journal (amended)",
		StartDate:    "2026-04-03",
		RecurrenceID: time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Delete the *instance* (not the series): this adds the EXDATE to the
	// master and soft-deletes the override.
	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete override: %v", err)
	}
	afterDelete, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master after delete: %v", err)
	}
	if afterDelete.ExDates == "" {
		t.Fatal("master.ExDates empty after override delete — EXDATE should have been added")
	}

	// Restore by UID (the `journals restore <uid>` path). Before the fix
	// this un-hid the override but left the stale EXDATE on the master.
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	afterRestore, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master after restore: %v", err)
	}
	if afterRestore.ExDates != "" {
		t.Fatalf("master.ExDates = %q, want empty after RestoreByUID", afterRestore.ExDates)
	}
	overrides, err := svc.ListOverridesByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("overrides after restore = %d, want 1", len(overrides))
	}
}

// TestSoftDelete_RestoreByUIDPreservesImportedExdate is the regression test
// for issue #86: an EXDATE that arrived via import (no delete added it) must
// survive a DeleteSeries + RestoreByUID round-trip, even when an override
// shares the same recurrence slot. RestoreByUID previously cleared the master
// EXDATE for every soft-deleted override's recurrence_id unconditionally,
// silently stripping the imported EXDATE.
func TestSoftDelete_RestoreByUIDPreservesImportedExdate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	slot := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "daily-uid", CalendarID: 1, Summary: "Daily Journal",
		StartDate:      "2026-04-01",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
		ExDates:        slot, // imported exclusion, not delete-added
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	_, err = svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Summary: "Daily Journal (amended)",
		StartDate:    "2026-04-03",
		RecurrenceID: slot,
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// DeleteSeries soft-deletes master + override WITHOUT adding any EXDATE.
	if err := svc.DeleteSeries(ctx, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	afterRestore, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master after restore: %v", err)
	}
	if afterRestore.ExDates == "" {
		t.Fatalf("imported EXDATE at %s was stripped by RestoreByUID", slot)
	}
}

// TestSoftDelete_UpsertClearsDeletedAt verifies UpsertByUID on a soft-
// deleted journal re-hydrates it (ON CONFLICT clears deleted_at).
func TestSoftDelete_UpsertClearsDeletedAt(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	created, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "upsert-uid", CalendarID: 1, Summary: "Original", StartDate: "2026-04-01",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, created.ID); err == nil {
		t.Fatal("row should be hidden after Delete")
	}

	revived, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "upsert-uid", CalendarID: 1, Summary: "Revived", StartDate: "2026-04-01",
	})
	if err != nil {
		t.Fatalf("UpsertByUID revive: %v", err)
	}
	if revived.ID != created.ID {
		t.Fatalf("upsert returned new ID %d, want same row %d", revived.ID, created.ID)
	}
	if _, err := svc.Get(ctx, created.ID); err != nil {
		t.Fatalf("Get after upsert revive: %v", err)
	}
}

// TestSoftDelete_SequenceBumpedOnRestore verifies Restore bumps sequence
// so synced journals push cleanly.
func TestSoftDelete_SequenceBumpedOnRestore(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)
	originalSeq := j.Sequence

	if err := svc.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.RestoreByID(ctx, j.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	restored, err := svc.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if restored.Sequence <= originalSeq {
		t.Fatalf("Sequence not bumped: before=%d after=%d", originalSeq, restored.Sequence)
	}
}

// TestSoftDelete_RestoreSurfacesTombstoneClearError verifies that a failure
// to clear the queued tombstone during restore is propagated to the caller
// instead of being silently swallowed. Regression test for #121.
func TestSoftDelete_RestoreSurfacesTombstoneClearError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)

	if err := svc.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Force the tombstone-clear inside reconcileSyncAfterRestore to fail.
	if _, err := svc.db.ExecContext(ctx, "DROP TABLE tombstones"); err != nil {
		t.Fatalf("drop tombstones: %v", err)
	}

	if err := svc.RestoreByID(ctx, j.ID); err == nil {
		t.Fatal("RestoreByID returned nil, want error when tombstone clear fails")
	}
}

// TestSoftDelete_OverrideMasterLookupError is a regression test for issue
// #290: deleting an override must not collapse a genuine DB error from the
// master lookup into the "no master" path. On a non-ErrNoRows error the old
// code soft-deleted the override while silently skipping the EXDATE and
// provenance bookkeeping, resurrecting the occurrence via series expansion.
func TestSoftDelete_OverrideMasterLookupError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "weekly-uid", CalendarID: 1, Summary: "Weekly Review",
		StartDate:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Summary: "Weekly Review (moved)",
		StartDate:    time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		RecurrenceID: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Force the master lookup (GetJournalByUID) to fail with a genuine, non-
	// ErrNoRows error by writing non-numeric text into the master's integer
	// sequence column so its row scan fails. The override row that the initial
	// Get(id) loads is untouched, and SoftDeleteJournal never scans, so the
	// buggy path would still soft-delete the override and return nil.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE journals SET sequence = 'corrupt' WHERE id = ?", master.ID); err != nil {
		t.Fatalf("corrupt master sequence: %v", err)
	}

	if err := svc.Delete(ctx, override.ID); err == nil {
		t.Fatal("Delete should propagate a non-ErrNoRows master-lookup error, got nil")
	}

	if _, err := svc.db.ExecContext(ctx,
		"UPDATE journals SET sequence = 0 WHERE id = ?", master.ID); err != nil {
		t.Fatalf("repair master sequence: %v", err)
	}
	if _, err := svc.Get(ctx, override.ID); err != nil {
		t.Fatalf("override should still be live after failed delete: %v", err)
	}
}

// TestDeleteSeries_MasterLookupError is a regression test for issue #290:
// DeleteSeries must not treat a genuine DB error from the master lookup as
// "no master". On a non-ErrNoRows error the old code soft-deleted the series
// locally with no tombstone and no dirty mark, so the next push never DELETEd
// the server copy and the series resurfaced on the next pull.
func TestDeleteSeries_MasterLookupError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "weekly-uid", CalendarID: 1, Summary: "Weekly",
		StartDate:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	if _, err := svc.db.ExecContext(ctx,
		"UPDATE journals SET sequence = 'corrupt' WHERE id = ?", master.ID); err != nil {
		t.Fatalf("corrupt master sequence: %v", err)
	}

	if err := svc.DeleteSeries(ctx, master.UID); err == nil {
		t.Fatal("DeleteSeries should propagate a non-ErrNoRows master-lookup error, got nil")
	}

	if _, err := svc.db.ExecContext(ctx,
		"UPDATE journals SET sequence = 0 WHERE id = ?", master.ID); err != nil {
		t.Fatalf("repair master sequence: %v", err)
	}
	if _, err := svc.GetByUID(ctx, master.UID); err != nil {
		t.Fatalf("series should still be live after failed DeleteSeries: %v", err)
	}
}
