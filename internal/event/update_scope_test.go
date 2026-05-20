package event

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newRecurringMaster(t *testing.T, svc *Service, uid string, rule string) Event {
	t.Helper()
	master, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID:            uid,
		CalendarID:     1,
		Title:          "Standup",
		StartTime:      time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC), // Mon
		EndTime:        time.Date(2026, 5, 18, 9, 30, 0, 0, time.UTC),
		RecurrenceRule: rule,
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	return master
}

func TestUpdateInstance_CreatesOverrideAndLeavesMasterIntact(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "scope-uid-1", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	instance := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC) // Wed instance
	_, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Standup (moved)",
		StartTime:  time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}

	override, err := svc.GetByUIDAndRecurrenceID(ctx, master.UID, "2026-05-20T09:00:00Z")
	if err != nil {
		t.Fatalf("override not found: %v", err)
	}
	if override.Title != "Standup (moved)" {
		t.Errorf("override.Title = %q, want %q", override.Title, "Standup (moved)")
	}
	if !override.StartTime.Equal(time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("override.StartTime = %v, want 9:30", override.StartTime)
	}
	if override.RecurrenceRule != "" {
		t.Errorf("override.RecurrenceRule = %q, want empty", override.RecurrenceRule)
	}

	fresh, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if fresh.Title != master.Title {
		t.Errorf("master.Title changed: got %q, want %q", fresh.Title, master.Title)
	}
	if !fresh.StartTime.Equal(master.StartTime) {
		t.Errorf("master.StartTime changed: got %v, want %v", fresh.StartTime, master.StartTime)
	}
	if fresh.RecurrenceRule != master.RecurrenceRule {
		t.Errorf("master.RecurrenceRule changed: got %q, want %q", fresh.RecurrenceRule, master.RecurrenceRule)
	}
}

func TestUpdateInstance_UpdatesExistingOverride(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "scope-uid-2", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	instance := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)

	// First edit creates the override.
	if _, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "First edit",
		StartTime:  time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("first UpdateInstance: %v", err)
	}

	// Second edit reuses the same override row.
	if _, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Second edit",
		StartTime:  time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 30, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("second UpdateInstance: %v", err)
	}

	overrides, err := svc.ListOverridesByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("expected 1 override, got %d", len(overrides))
	}
	if overrides[0].Title != "Second edit" {
		t.Errorf("override.Title = %q, want %q", overrides[0].Title, "Second edit")
	}
}

func TestUpdateFromInstance_TruncatesMasterAndCreatesSplit(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "split-uid-1", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	cutoff := time.Date(2026, 5, 27, 9, 0, 0, 0, time.UTC) // Wed of next week

	split, err := svc.UpdateFromInstance(ctx, master.UID, cutoff, UpdateParams{
		CalendarID:     master.CalendarID,
		Title:          "Standup (new room)",
		StartTime:      cutoff,
		EndTime:        cutoff.Add(45 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH",
	})
	if err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}

	// Original master is truncated to before the cutoff.
	old, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get old master: %v", err)
	}
	if !strings.Contains(strings.ToUpper(old.RecurrenceRule), "UNTIL=") {
		t.Errorf("expected UNTIL on truncated master, got %q", old.RecurrenceRule)
	}
	if strings.Contains(strings.ToUpper(old.RecurrenceRule), "COUNT=") {
		t.Errorf("expected COUNT to be stripped from truncated master, got %q", old.RecurrenceRule)
	}

	// Split row exists with the new title and a fresh UID.
	if split.UID == master.UID {
		t.Errorf("split UID should differ from old master UID")
	}
	if split.Title != "Standup (new room)" {
		t.Errorf("split.Title = %q", split.Title)
	}
	if split.RecurrenceRule != "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH" {
		t.Errorf("split rule lost: got %q", split.RecurrenceRule)
	}
	if !split.StartTime.Equal(cutoff) {
		t.Errorf("split.StartTime = %v, want %v", split.StartTime, cutoff)
	}
}

func TestUpdateFromInstance_SoftDeletesFutureOverrides(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "split-uid-2", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	// Override before cutoff — should survive.
	pastInstance := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC) // Tue
	if _, err := svc.UpdateInstance(ctx, master.UID, pastInstance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Tue override (kept)",
		StartTime:  pastInstance,
		EndTime:    pastInstance.Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("create past override: %v", err)
	}

	// Override at/after cutoff — should be soft-deleted.
	futureInstance := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC) // Thu next week
	if _, err := svc.UpdateInstance(ctx, master.UID, futureInstance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Thu override (dropped)",
		StartTime:  futureInstance,
		EndTime:    futureInstance.Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("create future override: %v", err)
	}

	cutoff := time.Date(2026, 5, 27, 9, 0, 0, 0, time.UTC) // Wed next week
	if _, err := svc.UpdateFromInstance(ctx, master.UID, cutoff, UpdateParams{
		CalendarID:     master.CalendarID,
		Title:          "split",
		StartTime:      cutoff,
		EndTime:        cutoff.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH",
	}); err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}

	overrides, err := svc.ListOverridesByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("expected 1 surviving override, got %d", len(overrides))
	}
	if overrides[0].Title != "Tue override (kept)" {
		t.Errorf("wrong override survived: got %q", overrides[0].Title)
	}
}

func TestUpdateInstance_CarriesMasterCategoriesWhenEmpty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "cats-uid-1", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")
	if err := svc.ReplaceCategories(ctx, master.ID, []string{"work", "team"}); err != nil {
		t.Fatalf("seed master categories: %v", err)
	}

	instance := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)
	override, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Standup (moved)",
		StartTime:  time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
		// Categories intentionally left empty — should inherit from master.
	})
	if err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}

	cats, err := svc.ListCategories(ctx, override.ID)
	if err != nil {
		t.Fatalf("list override categories: %v", err)
	}
	if len(cats) != 2 {
		t.Fatalf("expected 2 inherited categories, got %d: %v", len(cats), cats)
	}
}

func TestUpdateFromInstance_CarriesMasterCategoriesWhenEmpty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "cats-uid-2", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")
	if err := svc.ReplaceCategories(ctx, master.ID, []string{"work", "sync"}); err != nil {
		t.Fatalf("seed master categories: %v", err)
	}

	cutoff := time.Date(2026, 5, 27, 9, 0, 0, 0, time.UTC)
	split, err := svc.UpdateFromInstance(ctx, master.UID, cutoff, UpdateParams{
		CalendarID:     master.CalendarID,
		Title:          "Standup (new room)",
		StartTime:      cutoff,
		EndTime:        cutoff.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH",
		// Categories intentionally empty — should inherit from old master.
	})
	if err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}

	cats, err := svc.ListCategories(ctx, split.ID)
	if err != nil {
		t.Fatalf("list split categories: %v", err)
	}
	if len(cats) != 2 {
		t.Fatalf("expected 2 inherited categories, got %d: %v", len(cats), cats)
	}
}

func TestUpdate_AllEventsRewritesMaster(t *testing.T) {
	// Sanity guard that the existing Update path (used by the "All events"
	// scope) still rewrites the master row in place rather than creating an
	// override.
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "all-uid-1", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	if _, err := svc.Update(ctx, master.ID, UpdateParams{
		CalendarID:     master.CalendarID,
		Title:          "Standup (renamed)",
		StartTime:      master.StartTime,
		EndTime:        master.EndTime,
		RecurrenceRule: master.RecurrenceRule,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	overrides, err := svc.ListOverridesByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 0 {
		t.Errorf("expected no overrides after Update on master, got %d", len(overrides))
	}

	fresh, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	if fresh.Title != "Standup (renamed)" {
		t.Errorf("master.Title = %q, want %q", fresh.Title, "Standup (renamed)")
	}
}
