package event

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// TestSoftDelete_Standalone verifies:
//   - Delete sets deleted_at, row stays in DB
//   - Get returns ErrNoRows (filtered)
//   - ListDeleted surfaces it
//   - RestoreByID un-hides it
func TestSoftDelete_Standalone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	if err := svc.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := svc.Get(ctx, e.ID); err == nil {
		t.Fatal("Get should fail after soft-delete")
	}

	deleted, err := svc.ListDeleted(ctx, e.CalendarID)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 1 || deleted[0].ID != e.ID {
		t.Fatalf("ListDeleted = %+v, want one row with id %d", deleted, e.ID)
	}
	if deleted[0].DeletedAt == nil {
		t.Fatal("DeletedAt should be populated on soft-deleted row")
	}

	if err := svc.RestoreByID(ctx, e.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if _, err := svc.Get(ctx, e.ID); err != nil {
		t.Fatalf("Get after restore: %v", err)
	}
}

// TestSoftDelete_Series verifies DeleteSeries soft-deletes master + overrides
// and RestoreByUID un-hides them all.
func TestSoftDelete_Series(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "standup-uid",
		CalendarID:     1,
		Title:          "Daily Standup",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 9, 15, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	// Add an override on day 3.
	_, err = svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Daily Standup (moved)",
		StartTime:    time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 3, 10, 30, 0, 0, time.UTC),
		RecurrenceID: time.Date(2026, 4, 3, 9, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	if err := svc.DeleteSeries(ctx, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}

	// Both master and override should be in ListDeleted.
	deleted, err := svc.ListDeleted(ctx, 1)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("ListDeleted = %d, want 2", len(deleted))
	}

	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if _, err := svc.Get(ctx, master.ID); err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	overrides, err := svc.ListOverridesByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("overrides after restore = %d, want 1", len(overrides))
	}
}

// TestSoftDelete_RestoreByUIDNotDeleted verifies RestoreByUID reports
// ErrNotDeleted when nothing was actually restored — a live UID and an
// unknown UID both match zero soft-deleted rows. Without this the CLI
// silently prints "Restored event(s) ..." even though no row changed.
func TestSoftDelete_RestoreByUIDNotDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc) // live, never deleted

	if err := svc.RestoreByUID(ctx, e.UID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreByUID(live uid) = %v, want ErrNotDeleted", err)
	}
	if err := svc.RestoreByUID(ctx, "no-such-uid"); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreByUID(missing uid) = %v, want ErrNotDeleted", err)
	}
}

func TestSoftDelete_RestoreOverrideByIDClearsMasterEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "restore-override-exdate",
		CalendarID:     1,
		Title:          "Weekly Review",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Review (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete override: %v", err)
	}
	deletedMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after delete: %v", err)
	}
	if got := len(deletedMaster.ParseExDates()); got != 1 {
		t.Fatalf("EXDATE count after delete = %d, want 1", got)
	}

	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID override: %v", err)
	}
	if _, err := svc.Get(ctx, override.ID); err != nil {
		t.Fatalf("Get override after restore: %v", err)
	}
	restoredMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	if got := len(restoredMaster.ParseExDates()); got != 0 {
		t.Fatalf("EXDATE count after restore = %d, want 0 (%q)", got, restoredMaster.ExDates)
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
		UID:            "malformed-recid",
		CalendarID:     1,
		Title:          "Weekly Review",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Review (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Simulate corrupt/imported data: a non-parseable recurrence_id.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET recurrence_id = ? WHERE id = ?", "not-a-date", override.ID); err != nil {
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

func TestSoftDelete_RestoreByUIDClearsOverrideEXDATEs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "restore-uid-exdates",
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Sync (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete override: %v", err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	restoredMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	if got := len(restoredMaster.ParseExDates()); got != 0 {
		t.Fatalf("EXDATE count after RestoreByUID = %d, want 0 (%q)", got, restoredMaster.ExDates)
	}
}

func TestSoftDelete_RestoreUndoDeleteInstanceClearsEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "undo-instance-exdate",
		CalendarID:     1,
		Title:          "Office Hours",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	instance := time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC)

	meta, err := svc.DeleteInstanceWithUndo(ctx, master.UID, instance)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	deletedMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after delete: %v", err)
	}
	if got := len(deletedMaster.ParseExDates()); got != 1 {
		t.Fatalf("EXDATE count after delete = %d, want 1", got)
	}

	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	restoredMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	if got := len(restoredMaster.ParseExDates()); got != 0 {
		t.Fatalf("EXDATE count after RestoreUndo = %d, want 0 (%q)", got, restoredMaster.ExDates)
	}
	trash, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(trash) != 0 {
		t.Fatalf("trash entries after RestoreUndo = %d, want 0", len(trash))
	}
}

// TestSoftDelete_FromInstance_RRULE verifies DeleteFromInstanceWithUndo
// captures the pre-truncation RRULE and RestoreUndo rewrites it back.
func TestSoftDelete_FromInstance_RRULE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "sprint-review-uid",
		CalendarID:     1,
		Title:          "Sprint Review",
		StartTime:      time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	originalRRULE := master.RecurrenceRule

	// Add an override on week 4 which will be soft-deleted by truncation.
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Sprint Review (moved)",
		StartTime:    time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 22, 16, 0, 0, 0, time.UTC),
		RecurrenceID: time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	cutoff := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, master.UID, cutoff)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}
	if meta.Kind != UndoKindFromInstance {
		t.Fatalf("meta.Kind = %v, want UndoKindFromInstance", meta.Kind)
	}

	// Verify the master's RRULE was truncated, override soft-deleted.
	newMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after truncate: %v", err)
	}
	if newMaster.RecurrenceRule == originalRRULE {
		t.Fatalf("RRULE not truncated: still %q", newMaster.RecurrenceRule)
	}

	// Override should not be in live list but should be in deleted list.
	if _, err := svc.Get(ctx, override.ID); err == nil {
		t.Fatal("Get override should fail after truncate")
	}

	// Restore via RestoreUndo should rewrite RRULE back.
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	restoredMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	if restoredMaster.RecurrenceRule != originalRRULE {
		t.Fatalf("RRULE not restored: got %q, want %q", restoredMaster.RecurrenceRule, originalRRULE)
	}
	if _, err := svc.Get(ctx, override.ID); err != nil {
		t.Fatalf("Get override after restore: %v", err)
	}
}

// TestSoftDelete_FromInstance_UndoAfterGap reproduces issue #66: when the
// master's last real edit predates the truncation by more than one second,
// the stale-master guard used to mistake the truncation's own updated_at bump
// for a concurrent external edit and reject the Undo. The guard must compare
// against the master's POST-truncation updated_at, so Undo succeeds here while
// still detecting genuine later edits.
func TestSoftDelete_FromInstance_UndoAfterGap(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "gap-review-uid",
		CalendarID:     1,
		Title:          "Sprint Review",
		StartTime:      time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	originalRRULE := master.RecurrenceRule

	// Backdate the master's updated_at well into the past so its last real edit
	// is more than one second before the truncation — the production scenario
	// the old guard failed on.
	pastEdit := time.Now().UTC().Add(-1 * time.Hour).Format(timeutil.StorageTimeFormat)
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET updated_at = ? WHERE id = ?", pastEdit, master.ID); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}

	cutoff := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, master.UID, cutoff)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}

	// The captured baseline must be the truncation's own write (not the stale
	// pre-truncation value), so it equals the master's current updated_at.
	var dbUpdated string
	if err := svc.db.QueryRowContext(ctx,
		"SELECT updated_at FROM events WHERE id = ?", master.ID).Scan(&dbUpdated); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if got := meta.MasterUpdatedBefore; !got.Equal(parseStorageTime(dbUpdated)) {
		t.Fatalf("MasterUpdatedBefore = %s, want %s (the truncation's own write)",
			got.Format(time.RFC3339), dbUpdated)
	}
	if !meta.MasterUpdatedBefore.After(parseStorageTime(pastEdit)) {
		t.Fatal("MasterUpdatedBefore did not advance past the backdated edit")
	}

	// Undo must succeed even though the master's prior edit was >1s ago.
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo after gap: %v", err)
	}
	restored, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	if restored.RecurrenceRule != originalRRULE {
		t.Fatalf("RRULE not restored: got %q, want %q", restored.RecurrenceRule, originalRRULE)
	}

	// The guard must still catch a genuine concurrent edit. Re-truncate to get
	// fresh undo meta, then advance updated_at past the captured post-truncation
	// value to simulate an external write, and confirm Undo is rejected.
	meta2, err := svc.DeleteFromInstanceWithUndo(ctx, master.UID, cutoff)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo (2): %v", err)
	}
	future := time.Now().UTC().Add(1 * time.Hour).Format(timeutil.StorageTimeFormat)
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET updated_at = ? WHERE id = ?", future, master.ID); err != nil {
		t.Fatalf("simulate concurrent edit: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta2); err == nil {
		t.Fatal("RestoreUndo should reject a master edited after truncation")
	}
}

// TestSoftDelete_PurgeDeleted drops rows past the retention window and
// leaves fresh soft-deletes alone.
func TestSoftDelete_PurgeDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)
	if err := svc.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Purge with a cutoff in the future — every soft-deleted row should be
	// hard-deleted regardless of when it was soft-deleted (deleted_at < cutoff).
	future := time.Now().Add(time.Hour)
	n, err := svc.PurgeDeleted(ctx, future)
	if err != nil {
		t.Fatalf("PurgeDeleted: %v", err)
	}
	if n != 1 {
		t.Fatalf("PurgeDeleted returned %d, want 1", n)
	}

	deleted, err := svc.ListDeleted(ctx, e.CalendarID)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("ListDeleted after purge = %d, want 0", len(deleted))
	}
}

// TestSoftDelete_RestoreByID_ErrNotDeleted returns ErrNotDeleted for live rows.
func TestSoftDelete_RestoreByID_ErrNotDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)
	if err := svc.RestoreByID(ctx, e.ID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreByID on live row err = %v, want ErrNotDeleted", err)
	}

	// After hard-miss (non-existent ID), also ErrNotDeleted.
	if err := svc.RestoreByID(ctx, 999_999); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreByID on missing row err = %v, want ErrNotDeleted", err)
	}
}

// TestSoftDelete_ListFilteredExcludesDeleted verifies the dynamic read path
// (ListEventsFiltered / ListRecurringEventsFiltered) honors deleted_at by
// default and the IncludeDeleted opt-in.
func TestSoftDelete_ListFilteredExcludesDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)
	if err := svc.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	live, err := svc.ListByDateRange(ctx,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("ListByDateRange: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("Live range list should exclude deleted rows, got %d", len(live))
	}
}

// TestSoftDelete_PurgeByID_RefusesLiveRow verifies PurgeByID only drops
// soft-deleted rows and returns ErrNotDeleted otherwise, so a caller can't
// hard-delete a live event by passing the wrong ID.
func TestSoftDelete_PurgeByID_RefusesLiveRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	if err := svc.PurgeByID(ctx, e.ID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("PurgeByID on live row err = %v, want ErrNotDeleted", err)
	}
	if _, err := svc.Get(ctx, e.ID); err != nil {
		t.Fatalf("live row should still be readable: %v", err)
	}

	if err := svc.PurgeByID(ctx, 999_999); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("PurgeByID on missing row err = %v, want ErrNotDeleted", err)
	}

	if err := svc.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.PurgeByID(ctx, e.ID); err != nil {
		t.Fatalf("PurgeByID on soft-deleted row: %v", err)
	}
	deleted, err := svc.ListDeleted(ctx, e.CalendarID)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("ListDeleted after PurgeByID = %d, want 0", len(deleted))
	}
}

// TestSoftDelete_SequenceBumpedOnRestore verifies Restore bumps sequence
// so synced rows push cleanly to CalDAV servers.
func TestSoftDelete_SequenceBumpedOnRestore(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)
	originalSeq := e.Sequence

	if err := svc.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.RestoreByID(ctx, e.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	restored, err := svc.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if restored.Sequence <= originalSeq {
		t.Fatalf("Sequence not bumped: before=%d after=%d", originalSeq, restored.Sequence)
	}
}

// TestSoftDelete_UndoInstanceDeletePreservesPreexistingEXDATE verifies that
// undoing a single-instance delete removes only the EXDATE that delete added,
// leaving a pre-existing exclusion for the same slot intact. The "EXDATE +
// live override at the same slot" shape arrives via import/sync; without
// remove-one semantics the undo would strip both EXDATEs and the base
// occurrence could resurface once the override is later removed.
func TestSoftDelete_UndoInstanceDeletePreservesPreexistingEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const slot = "2026-04-08T10:00:00Z"
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "preexisting-exdate",
		CalendarID:     1,
		Title:          "Weekly Review",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
		ExDates:        slot, // pre-existing exclusion at the same slot
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Review (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: slot,
	}); err != nil {
		t.Fatalf("create override: %v", err)
	}

	instance := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	meta, err := svc.DeleteInstanceWithUndo(ctx, master.UID, instance)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}

	restoredMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	if got := len(restoredMaster.ParseExDates()); got != 1 {
		t.Fatalf("EXDATE count after undo = %d, want 1 (pre-existing exclusion preserved) (%q)", got, restoredMaster.ExDates)
	}
}

// TestSoftDelete_RestoreByUIDPreservesImportedEXDATE is the regression test for
// issue #86: an EXDATE that arrived via import (no delete added it) must
// survive a DeleteSeries + RestoreByUID round-trip, even when an override
// shares the same recurrence slot. RestoreByUID previously cleared the master
// EXDATE for every soft-deleted override's recurrence_id unconditionally,
// silently stripping the imported EXDATE.
func TestSoftDelete_RestoreByUIDPreservesImportedEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const slot = "2026-04-08T10:00:00Z"
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "imported-exdate-restore",
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
		ExDates:        slot, // imported exclusion, not delete-added
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Sync (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: slot,
	}); err != nil {
		t.Fatalf("create override: %v", err)
	}

	// DeleteSeries soft-deletes master + override WITHOUT adding any EXDATE.
	if err := svc.DeleteSeries(ctx, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	restoredMaster, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	if got := len(restoredMaster.ParseExDates()); got != 1 {
		t.Fatalf("EXDATE count after RestoreByUID = %d, want 1 (imported exclusion preserved) (%q)", got, restoredMaster.ExDates)
	}
}

// TestSoftDelete_RestoreFromInstanceSurfacesMarkDirtyError verifies that a
// failure to mark the master resource dirty during a from-instance undo is
// propagated to the caller instead of being silently swallowed. Without it the
// restored occurrences reappear locally but are never re-synced to the server.
// Regression test for #252.
func TestSoftDelete_RestoreFromInstanceSurfacesMarkDirtyError(t *testing.T) {
	svc := newTestService(t)
	makeSyncedCalendar(t, svc)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "restore-from-instance-markdirty",
		CalendarID:     1,
		Title:          "Sprint Review",
		StartTime:      time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	cutoff := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, master.UID, cutoff)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}

	// Force the dirty-mark inside restoreFromInstance to fail.
	if _, err := svc.db.ExecContext(ctx, "DROP TABLE sync_resources"); err != nil {
		t.Fatalf("drop sync_resources: %v", err)
	}

	if err := svc.RestoreUndo(ctx, meta); err == nil {
		t.Fatal("RestoreUndo returned nil, want error when MarkResourceDirty fails")
	}
}

// TestSoftDelete_RestoreSurfacesTombstoneClearError verifies that a failure
// to clear the queued tombstone during restore is propagated to the caller
// instead of being silently swallowed. Regression test for #121.
func TestSoftDelete_RestoreSurfacesTombstoneClearError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	if err := svc.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Force the tombstone-clear inside reconcileSyncAfterRestore to fail.
	if _, err := svc.db.ExecContext(ctx, "DROP TABLE tombstones"); err != nil {
		t.Fatalf("drop tombstones: %v", err)
	}

	if err := svc.RestoreByID(ctx, e.ID); err == nil {
		t.Fatal("RestoreByID returned nil, want error when tombstone clear fails")
	}
}

// TestSoftDelete_OverrideMasterLookupError is the regression test for issue
// #412: deleting an override must not collapse a genuine DB error from the
// master lookup into the "no master" path. On a non-ErrNoRows error the old
// code soft-deleted the override while silently skipping the EXDATE and
// provenance bookkeeping, resurrecting the occurrence via series expansion and
// leaving it unrestorable via trash. Same failure mode as #290, fixed there in
// the todo and journal services but never in the event service.
func TestSoftDelete_OverrideMasterLookupError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "weekly-uid",
		CalendarID:     1,
		Title:          "Weekly Review",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Review (moved)",
		StartTime:    time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 15, 15, 0, 0, 0, time.UTC),
		RecurrenceID: time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Force the master lookup (GetEventByUID) to fail with a genuine, non-
	// ErrNoRows error by writing non-numeric text into the master's integer
	// sequence column so its row scan fails. The override row that the initial
	// Get(id) loads is untouched, and SoftDeleteEvent never scans, so the buggy
	// path would still soft-delete the override and return nil.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET sequence = 'corrupt' WHERE id = ?", master.ID); err != nil {
		t.Fatalf("corrupt master sequence: %v", err)
	}

	if err := svc.Delete(ctx, override.ID); err == nil {
		t.Fatal("Delete should propagate a non-ErrNoRows master-lookup error, got nil")
	}

	// Repair the master so reads work, then confirm the override is still
	// live: the transaction must have rolled back.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET sequence = 0 WHERE id = ?", master.ID); err != nil {
		t.Fatalf("repair master sequence: %v", err)
	}
	if _, err := svc.Get(ctx, override.ID); err != nil {
		t.Fatalf("override should still be live after failed delete: %v", err)
	}
}

// TestSoftDelete_FromInstanceUndo_ReAddsRDates reproduces issue #490: the TUI
// truncation-undo path (RestoreUndo of an UndoKindFromInstance) must re-add the
// post-cutoff RDATEs the truncation trimmed, mirroring the trash-restore path
// (issue #463). Before the fix, RestoreUndo rewrote only the RRULE and silently
// dropped the trimmed RDATEs.
func TestSoftDelete_FromInstanceUndo_ReAddsRDates(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	master := newRDateOnlyMaster(t, svc, "rdate-undo-readds", []time.Time{rdate1, rdate2})

	meta, err := svc.DeleteFromInstanceWithUndo(ctx, master.UID, rdate2)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}

	// The truncation trimmed the post-cutoff RDATE.
	trimmed, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after truncate: %v", err)
	}
	if got := trimmed.ParseRDates(); len(got) != 1 || !got[0].Equal(rdate1) {
		t.Fatalf("RDates after truncate = %v, want only %s", got, rdate1.Format(time.RFC3339))
	}

	// Undo must put the dropped RDATE back.
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	restored, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after undo: %v", err)
	}
	rdates := restored.ParseRDates()
	foundR2 := false
	for _, rd := range rdates {
		if rd.Equal(rdate2) {
			foundR2 = true
		}
	}
	if len(rdates) != 2 || !foundR2 {
		t.Fatalf("issue #490: RDates after undo = %v, want both restored (incl. %s)",
			rdates, rdate2.Format(time.RFC3339))
	}
}

// TestSoftDelete_FromInstanceUndo_KeepsIndependentlyDeletedOverride reproduces
// issue #491 (the #287 class on the undo path): deleting a single override, then
// truncating "this and following" from an earlier cutoff, then Undo must NOT
// resurrect the independently-deleted override. Before the fix, RestoreUndo
// called RestoreEventsByUID, which un-hid every soft-deleted row sharing the UID.
func TestSoftDelete_FromInstanceUndo_KeepsIndependentlyDeletedOverride(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "standup-undo",
		CalendarID:     1,
		Title:          "Standup",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// User customizes the Apr 22 instance (creates an override).
	overrideTime := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Standup (moved)",
		StartTime:    time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 22, 10, 30, 0, 0, time.UTC),
		RecurrenceID: overrideTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// User deletes that single customized instance on its own.
	if err := svc.DeleteInstance(ctx, master.UID, overrideTime); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if _, err := svc.Get(ctx, override.ID); err == nil {
		t.Fatalf("override should be soft-deleted after DeleteInstance")
	}

	// Later, user truncates "this and following" from an earlier cutoff (Apr 8).
	cutoff := time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC)
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, master.UID, cutoff)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}

	// Undo the truncation. This must NOT resurrect the override the user
	// independently deleted before the truncation.
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if _, err := svc.Get(ctx, override.ID); err == nil {
		t.Fatalf("issue #491: independently-deleted override resurrected by truncation undo")
	}
}
