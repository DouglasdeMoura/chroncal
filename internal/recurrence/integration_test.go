package recurrence

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/testutil"
	"github.com/douglasdemoura/tcal/internal/todo"
)

func TestRecurringEventAlarmFlow(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)

	// Create weekly event with alarm
	base := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC) // Monday
	evt, err := eventsSvc.Create(context.Background(), event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      base,
		EndTime:        base.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO;COUNT=4",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Add alarm separately
	err = eventsSvc.ReplaceAlarms(context.Background(), evt.ID, []model.Alarm{{
		UID:          "weekly-alarm",
		Action:       "DISPLAY",
		TriggerValue: "-PT10M",
		Description:  "Weekly sync starting soon",
	}})
	if err != nil {
		t.Fatalf("add alarm: %v", err)
	}

	// Verify expansion works
	instances := ExpandEvent(evt, base.Add(-time.Hour), base.AddDate(0, 1, 0))
	if len(instances) != 4 {
		t.Errorf("instances = %d, want 4", len(instances))
	}

	// Cache instances
	if err := recurSvc.ExpandAndCache(context.Background(), evt,
		base.Add(-time.Hour), base.AddDate(0, 1, 0)); err != nil {
		t.Fatalf("cache: %v", err)
	}

	// Verify cached instances
	count, err := q.CountRecurrenceInstances(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 4 {
		t.Errorf("cached instances = %d, want 4", count)
	}
}

func TestListExpandedByDateRange(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring event with DTSTART far in the past (2020).
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Monday",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}

	// Non-recurring event inside query range.
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "One-off Meeting",
		StartTime:  time.Date(2026, 3, 31, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 3, 31, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	// Non-recurring event outside range — should not appear.
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Old Event",
		StartTime:  time.Date(2020, 6, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2020, 6, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Expect: Mon Mar 30, Tue Mar 31 (one-off), Mon Apr 6 = 3 events.
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	// Verify sorted by StartTime.
	if !sort.SliceIsSorted(events, func(i, j int) bool {
		return events[i].StartTime.Before(events[j].StartTime)
	}) {
		t.Error("events not sorted by StartTime")
	}

	// First should be the Monday Mar 30 instance.
	if events[0].Title != "Weekly Monday" {
		t.Errorf("events[0].Title = %q, want %q", events[0].Title, "Weekly Monday")
	}
	wantMar30 := time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC)
	if !events[0].StartTime.Equal(wantMar30) {
		t.Errorf("events[0].StartTime = %v, want %v", events[0].StartTime, wantMar30)
	}

	// Second should be the one-off on Mar 31.
	if events[1].Title != "One-off Meeting" {
		t.Errorf("events[1].Title = %q, want %q", events[1].Title, "One-off Meeting")
	}

	// Third should be the Monday Apr 6 instance.
	wantApr6 := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	if !events[2].StartTime.Equal(wantApr6) {
		t.Errorf("events[2].StartTime = %v, want %v", events[2].StartTime, wantApr6)
	}

	// EndTime should be adjusted (1 hour duration preserved).
	wantEnd := wantMar30.Add(time.Hour)
	if !events[0].EndTime.Equal(wantEnd) {
		t.Errorf("events[0].EndTime = %v, want %v", events[0].EndTime, wantEnd)
	}
}

func TestListExpandedByDateRange_ExDate(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	excluded := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC) // second Monday
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Except One",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		ExDates:        excluded.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Only 1 Monday (Mar 30) — Apr 6 is excluded.
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	for _, e := range events {
		if e.StartTime.Equal(excluded) {
			t.Error("excluded date appeared in results")
		}
	}
}

func TestListExpandedByDateRange_NoDuplication(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring event whose DTSTART is inside the query range.
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Daily For 3",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Should get exactly 3 instances, not duplicated.
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
}

func TestExportExpandedByDateRange(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring event from the past.
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Export",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Non-recurring event in range.
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "One-off Export",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ExportExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ExportExpandedByDateRange: %v", err)
	}

	// Should return 2 master events (not expanded instances).
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	// The recurring event should retain its original DTSTART (master, not instance).
	for _, e := range events {
		if e.Title == "Weekly Export" {
			if e.StartTime.Year() != 2020 {
				t.Errorf("export master StartTime.Year = %d, want 2020", e.StartTime.Year())
			}
			if e.RecurrenceRule == "" {
				t.Error("export master lost its RecurrenceRule")
			}
		}
	}
}

func TestListExpandedByDateRange_OverrideMerging(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Weekly Monday event.
	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Standup",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Override: move Apr 6 instance to Thursday Apr 9 at 14:00.
	_, err = eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Standup (moved)",
		StartTime:    time.Date(2026, 4, 9, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 9, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-06T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Expect: Mar 30 (original), Apr 9 (override), NOT Apr 6 (replaced).
	if len(events) != 2 {
		for i, e := range events {
			t.Logf("  events[%d]: %s at %v", i, e.Title, e.StartTime)
		}
		t.Fatalf("got %d events, want 2", len(events))
	}

	// First: Mar 30 original instance.
	if events[0].Title != "Weekly Standup" {
		t.Errorf("events[0].Title = %q, want %q", events[0].Title, "Weekly Standup")
	}
	wantMar30 := time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC)
	if !events[0].StartTime.Equal(wantMar30) {
		t.Errorf("events[0].StartTime = %v, want %v", events[0].StartTime, wantMar30)
	}

	// Second: Apr 9 override (not Apr 6).
	if events[1].Title != "Weekly Standup (moved)" {
		t.Errorf("events[1].Title = %q, want %q", events[1].Title, "Weekly Standup (moved)")
	}
	wantApr9 := time.Date(2026, 4, 9, 14, 0, 0, 0, time.UTC)
	if !events[1].StartTime.Equal(wantApr9) {
		t.Errorf("events[1].StartTime = %v, want %v", events[1].StartTime, wantApr9)
	}
}

func TestListExpandedByDateRange_CancelledOverride(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Daily",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Cancel the Apr 2 instance.
	_, err = eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Daily",
		StartTime:    time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-02T09:00:00Z",
		Status:       "CANCELLED",
	})
	if err != nil {
		t.Fatalf("create cancelled override: %v", err)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Should get 2 instances (Apr 1, Apr 3). Apr 2 is cancelled.
	if len(events) != 2 {
		for i, e := range events {
			t.Logf("  events[%d]: %s at %v (status=%s)", i, e.Title, e.StartTime, e.Status)
		}
		t.Fatalf("got %d events, want 2", len(events))
	}

	for _, e := range events {
		if e.StartTime.Day() == 2 {
			t.Error("cancelled Apr 2 instance appeared in results")
		}
	}
}

func TestListFilteredTodos_DefaultIncludesRecurring(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring weekly todo.
	_, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Weekly Review",
		DueDate:        "2020-01-06",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}

	// Non-recurring todo.
	_, err = todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "One-off Task",
		DueDate:    "2026-04-01",
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	// Default list with no date range must include recurring masters.
	todos, err := recurSvc.ListFilteredTodos(ctx, TodoListParams{})
	if err != nil {
		t.Fatalf("ListFilteredTodos: %v", err)
	}

	if len(todos) != 2 {
		for i, td := range todos {
			t.Logf("  todos[%d]: %s due=%s rrule=%s", i, td.Summary, td.DueDate, td.RecurrenceRule)
		}
		t.Fatalf("got %d todos, want 2", len(todos))
	}

	// Verify both are present.
	found := map[string]bool{}
	for _, td := range todos {
		found[td.Summary] = true
	}
	if !found["Weekly Review"] {
		t.Error("missing recurring todo 'Weekly Review'")
	}
	if !found["One-off Task"] {
		t.Error("missing one-off todo 'One-off Task'")
	}
}

func TestListFilteredTodos_FiltersApplyToRecurring(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring todo with NEEDS-ACTION status.
	_, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Active Recurring",
		DueDate:        "2020-01-06",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Recurring todo that is completed.
	completed, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Done Recurring",
		DueDate:        "2020-01-06",
		RecurrenceRule: "FREQ=DAILY",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	todoSvc.Complete(ctx, completed.ID)

	// Filter by status NEEDS-ACTION — only active recurring should appear.
	todos, err := recurSvc.ListFilteredTodos(ctx, TodoListParams{Status: "NEEDS-ACTION"})
	if err != nil {
		t.Fatalf("ListFilteredTodos: %v", err)
	}

	if len(todos) != 1 {
		for i, td := range todos {
			t.Logf("  todos[%d]: %s status=%s", i, td.Summary, td.Status)
		}
		t.Fatalf("got %d todos, want 1", len(todos))
	}
	if todos[0].Summary != "Active Recurring" {
		t.Errorf("Summary = %q, want %q", todos[0].Summary, "Active Recurring")
	}
}

func TestListExpandedTodosByDueDateRange(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring weekly todo with DUE in the past.
	_, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Weekly Review",
		DueDate:        "2020-01-06",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}

	// Non-recurring todo inside range.
	_, err = todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "One-off Task",
		DueDate:    "2026-04-01",
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	todos, err := recurSvc.ListExpandedTodosByDueDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedTodosByDueDateRange: %v", err)
	}

	// Expect: Mar 30 (recurring), Apr 1 (one-off), Apr 6 (recurring) = 3.
	// Apr 13 is excluded by half-open [from, to) semantics.
	if len(todos) != 3 {
		for i, td := range todos {
			t.Logf("  todos[%d]: %s due=%s", i, td.Summary, td.DueDate)
		}
		t.Fatalf("got %d todos, want 3", len(todos))
	}

	// First should be the Monday Mar 30 instance.
	if todos[0].Summary != "Weekly Review" {
		t.Errorf("todos[0].Summary = %q, want %q", todos[0].Summary, "Weekly Review")
	}
	if todos[0].DueDate != "2026-03-30" {
		t.Errorf("todos[0].DueDate = %q, want %q", todos[0].DueDate, "2026-03-30")
	}

	// Second should be the one-off on Apr 1.
	if todos[1].Summary != "One-off Task" {
		t.Errorf("todos[1].Summary = %q, want %q", todos[1].Summary, "One-off Task")
	}
}
