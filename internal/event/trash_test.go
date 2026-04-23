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

// TestTrash_TruncationLoggedAndRestorable verifies:
//   - DeleteFromInstance captures the pre-truncation RRULE in the log
//   - ListTrash surfaces the entry as TrashKindTruncation
//   - RestoreTrash rewrites the RRULE back AND un-hides soft-deleted overrides
func TestTrash_TruncationLoggedAndRestorable(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "sprint-review",
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

	// Add an override past the cutoff that will be soft-deleted by truncation.
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
	if err := svc.DeleteFromInstance(ctx, master.UID, cutoff); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}

	// The log row should surface as TrashKindTruncation (alongside the
	// soft-deleted override which surfaces as TrashKindEvent).
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	var trunc, evtEntry *TrashEntry
	for i := range entries {
		switch entries[i].Kind {
		case TrashKindTruncation:
			trunc = &entries[i]
		case TrashKindEvent:
			if entries[i].ID == override.ID {
				evtEntry = &entries[i]
			}
		}
	}
	if trunc == nil {
		t.Fatalf("ListTrash: no TrashKindTruncation entry, got %+v", entries)
	}
	if trunc.PreviousRRule != originalRRULE {
		t.Fatalf("PreviousRRule = %q, want %q", trunc.PreviousRRule, originalRRULE)
	}
	if !trunc.CutoffTime.Equal(cutoff) {
		t.Fatalf("CutoffTime = %v, want %v", trunc.CutoffTime, cutoff)
	}
	if evtEntry == nil {
		t.Fatalf("override %d should also appear as TrashKindEvent", override.ID)
	}

	// RRULE should currently be truncated (UNTIL set).
	m, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master: %v", err)
	}
	if m.RecurrenceRule == originalRRULE {
		t.Fatalf("RRULE not truncated: still %q", m.RecurrenceRule)
	}

	// Restore via the trash entry.
	if err := svc.RestoreTrash(ctx, *trunc); err != nil {
		t.Fatalf("RestoreTrash: %v", err)
	}
	m, err = svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master after restore: %v", err)
	}
	if m.RecurrenceRule != originalRRULE {
		t.Fatalf("RRULE not restored: got %q, want %q", m.RecurrenceRule, originalRRULE)
	}
	if _, err := svc.Get(ctx, override.ID); err != nil {
		t.Fatalf("override not restored: %v", err)
	}

	// Both entries should be gone from trash.
	entries, _ = svc.ListTrash(ctx, 1)
	if len(entries) != 0 {
		t.Fatalf("ListTrash after restore = %d, want 0 (%+v)", len(entries), entries)
	}
}

// TestTrash_PurgeTruncationKeepsRRuleTruncated verifies that purging a
// truncation entry drops the log row only — the master's RRULE stays
// truncated and soft-deleted overrides stay deleted.
func TestTrash_PurgeTruncationKeepsRRuleTruncated(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "weekly-trunc",
		CalendarID:     1,
		Title:          "Weekly trunc",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 10, 30, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=8",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	originalRRULE := master.RecurrenceRule
	cutoff := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	if err := svc.DeleteFromInstance(ctx, master.UID, cutoff); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}

	entries, _ := svc.ListTrash(ctx, 1)
	var trunc TrashEntry
	for _, e := range entries {
		if e.Kind == TrashKindTruncation {
			trunc = e
			break
		}
	}
	if trunc.ID == 0 {
		t.Fatalf("no truncation entry: %+v", entries)
	}

	if err := svc.PurgeTrashEntry(ctx, trunc); err != nil {
		t.Fatalf("PurgeTrashEntry: %v", err)
	}
	// Truncation entry is gone, but master's RRULE stays truncated.
	entries, _ = svc.ListTrash(ctx, 1)
	for _, e := range entries {
		if e.Kind == TrashKindTruncation {
			t.Fatalf("truncation entry still present after purge: %+v", e)
		}
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master: %v", err)
	}
	if m.RecurrenceRule == originalRRULE {
		t.Fatalf("RRULE un-truncated after purge: %q", m.RecurrenceRule)
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
