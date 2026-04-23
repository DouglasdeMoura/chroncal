package todo

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSoftDelete_Standalone verifies:
//   - Delete sets deleted_at, row stays in DB
//   - Get returns an error (filtered)
//   - ListDeleted surfaces it
//   - RestoreByID un-hides it
func TestSoftDelete_Standalone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)

	if err := svc.Delete(ctx, td.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := svc.Get(ctx, td.ID); err == nil {
		t.Fatal("Get should fail after soft-delete")
	}

	deleted, err := svc.ListDeleted(ctx, td.CalendarID)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 1 || deleted[0].ID != td.ID {
		t.Fatalf("ListDeleted = %+v, want one row with id %d", deleted, td.ID)
	}

	if err := svc.RestoreByID(ctx, td.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if _, err := svc.Get(ctx, td.ID); err != nil {
		t.Fatalf("Get after restore: %v", err)
	}
}

// TestSoftDelete_Series verifies DeleteSeries soft-deletes master + overrides
// and RestoreByUID un-hides them all.
func TestSoftDelete_Series(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "weekly-uid", CalendarID: 1, Summary: "Weekly Review",
		DueDate:        time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	_, err = svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Summary: "Weekly Review (moved)",
		DueDate:      time.Date(2026, 4, 15, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceID: time.Date(2026, 4, 15, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
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
	overrides, err := svc.ListOverridesByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("overrides after restore = %d, want 1", len(overrides))
	}
}

// TestSoftDelete_ListExcludesDeleted covers the live read path.
func TestSoftDelete_ListExcludesDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)
	if err := svc.Delete(ctx, td.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	rows, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		if r.ID == td.ID {
			t.Fatalf("List returned soft-deleted row %d", td.ID)
		}
	}
}

// TestSoftDelete_RestoreByID_ErrNotDeleted returns ErrNotDeleted for live rows
// and for missing rows, matching the event package contract.
func TestSoftDelete_RestoreByID_ErrNotDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)

	if err := svc.RestoreByID(ctx, td.ID); !errors.Is(err, ErrNotDeleted) {
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
	td := createTodo(t, svc)
	if err := svc.Delete(ctx, td.ID); err != nil {
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

	deleted, err := svc.ListDeleted(ctx, td.CalendarID)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("ListDeleted after purge = %d, want 0", len(deleted))
	}
}

// TestSoftDelete_PurgeByID_RefusesLiveRow verifies PurgeByID only drops
// soft-deleted rows.
func TestSoftDelete_PurgeByID_RefusesLiveRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)

	if err := svc.PurgeByID(ctx, td.ID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("PurgeByID on live row err = %v, want ErrNotDeleted", err)
	}
	if _, err := svc.Get(ctx, td.ID); err != nil {
		t.Fatalf("live row should still be readable: %v", err)
	}
	if err := svc.PurgeByID(ctx, 999_999); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("PurgeByID on missing row err = %v, want ErrNotDeleted", err)
	}

	if err := svc.Delete(ctx, td.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.PurgeByID(ctx, td.ID); err != nil {
		t.Fatalf("PurgeByID on soft-deleted row: %v", err)
	}
	deleted, err := svc.ListDeleted(ctx, td.CalendarID)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("ListDeleted after PurgeByID = %d, want 0", len(deleted))
	}
}

// TestSoftDelete_RestoreOverrideClearsExdate verifies that restoring a
// recurring override not only un-hides the row but also strips the
// matching EXDATE from the master — so recurrence expansion surfaces the
// occurrence again. Without this, the row would exist in the DB but
// stay hidden from live views.
func TestSoftDelete_RestoreOverrideClearsExdate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "weekly-uid", CalendarID: 1, Summary: "Weekly Review",
		DueDate:        time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Summary: "Weekly Review (moved)",
		DueDate:      time.Date(2026, 4, 15, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		RecurrenceID: time.Date(2026, 4, 15, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
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

// TestSoftDelete_UpsertClearsDeletedAt verifies that UpsertByUID on a
// soft-deleted row re-hydrates it (ON CONFLICT clears deleted_at). This
// is the path a remote re-CREATE after a local delete would take: the
// server sends a row with the old UID and the local should come back
// as live, not stay hidden.
func TestSoftDelete_UpsertClearsDeletedAt(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	created, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "upsert-uid", CalendarID: 1, Summary: "Original",
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
		UID: "upsert-uid", CalendarID: 1, Summary: "Revived",
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
// so synced todos push cleanly.
func TestSoftDelete_SequenceBumpedOnRestore(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	td := createTodo(t, svc)
	originalSeq := td.Sequence

	if err := svc.Delete(ctx, td.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.RestoreByID(ctx, td.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	restored, err := svc.Get(ctx, td.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if restored.Sequence <= originalSeq {
		t.Fatalf("Sequence not bumped: before=%d after=%d", originalSeq, restored.Sequence)
	}
}
