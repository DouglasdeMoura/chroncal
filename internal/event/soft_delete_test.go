package event

import (
	"context"
	"errors"
	"testing"
	"time"
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
	if meta.MasterRRuleBefore != originalRRULE {
		t.Fatalf("MasterRRuleBefore = %q, want %q", meta.MasterRRuleBefore, originalRRULE)
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
