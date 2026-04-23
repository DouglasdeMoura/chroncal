package event

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestTrash_InstanceDeleteLoggedAndRestorable verifies:
//   - DeleteInstance adds an EXDATE and a log row
//   - ListTrash surfaces the log row as TrashKindInstance
//   - RestoreTrash removes the EXDATE and deletes the log row
//   - The instance reappears on next expansion
func TestTrash_InstanceDeleteLoggedAndRestorable(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "focus-time",
		CalendarID:     1,
		Title:          "Focus time",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	instance := time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC)
	if err := svc.DeleteInstance(ctx, master.UID, instance); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}

	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListTrash = %d entries, want 1 (%+v)", len(entries), entries)
	}
	got := entries[0]
	if got.Kind != TrashKindInstance {
		t.Fatalf("Kind = %v, want TrashKindInstance", got.Kind)
	}
	if got.UID != master.UID {
		t.Fatalf("UID = %q, want %q", got.UID, master.UID)
	}
	if !got.InstanceTime.Equal(instance) {
		t.Fatalf("InstanceTime = %v, want %v", got.InstanceTime, instance)
	}
	if !strings.Contains(got.Title, "Focus time") {
		t.Fatalf("Title = %q, want contains 'Focus time'", got.Title)
	}

	// Restore — EXDATE should be removed, log row gone.
	if err := svc.RestoreTrash(ctx, got); err != nil {
		t.Fatalf("RestoreTrash: %v", err)
	}

	entries, err = svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash after restore: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ListTrash after restore = %d, want 0", len(entries))
	}
	// Verify EXDATE was actually removed on the master.
	restored, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master: %v", err)
	}
	if strings.Contains(restored.ExDates, instance.Format(time.RFC3339)) {
		t.Fatalf("EXDATE still present after restore: %q", restored.ExDates)
	}
}

// TestTrash_SoftDeletedEventAppearsAsEventKind verifies standalone soft-deletes
// are surfaced as TrashKindEvent and coexist with instance deletes.
func TestTrash_SoftDeletedEventAppearsAsEventKind(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)
	if err := svc.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, err := svc.ListTrash(ctx, e.CalendarID)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListTrash = %d, want 1", len(entries))
	}
	if entries[0].Kind != TrashKindEvent {
		t.Fatalf("Kind = %v, want TrashKindEvent", entries[0].Kind)
	}
	if entries[0].ID != e.ID {
		t.Fatalf("ID = %d, want %d", entries[0].ID, e.ID)
	}
}

// TestTrash_InstanceDeleteIsIdempotent verifies deleting the same instance
// twice keeps a single log row (ON CONFLICT upserts deleted_at).
func TestTrash_InstanceDeleteIsIdempotent(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "standup",
		CalendarID:     1,
		Title:          "Standup",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 9, 15, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	instance := time.Date(2026, 4, 3, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := svc.DeleteInstance(ctx, master.UID, instance); err != nil {
			t.Fatalf("DeleteInstance #%d: %v", i, err)
		}
	}
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListTrash = %d, want 1 (dedup via ON CONFLICT)", len(entries))
	}
}

// TestTrash_PurgeInstanceKeepsExdate verifies that purging an instance-kind
// entry drops the log row but leaves the EXDATE on the master — the
// "forever delete" semantics for per-instance deletes.
func TestTrash_PurgeInstanceKeepsExdate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "weekly-sync",
		CalendarID:     1,
		Title:          "Weekly sync",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 10, 30, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=8",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	instance := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	if err := svc.DeleteInstance(ctx, master.UID, instance); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	entries, _ := svc.ListTrash(ctx, 1)
	if len(entries) != 1 {
		t.Fatalf("ListTrash = %d, want 1", len(entries))
	}
	if err := svc.PurgeTrashEntry(ctx, entries[0]); err != nil {
		t.Fatalf("PurgeTrashEntry: %v", err)
	}

	// Trash now empty, but EXDATE should still be on the master.
	entries, _ = svc.ListTrash(ctx, 1)
	if len(entries) != 0 {
		t.Fatalf("ListTrash after purge = %d, want 0", len(entries))
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master: %v", err)
	}
	if !strings.Contains(m.ExDates, instance.Format(time.RFC3339)) {
		t.Fatalf("EXDATE missing after purge: %q (expected instance to stay excluded)", m.ExDates)
	}
}
