package journal

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
