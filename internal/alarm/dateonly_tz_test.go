package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestTodoAlarm_DateOnlyDueEveryZone guards issue #875. A date-only DUE is
// a floating date (RFC 5545 section 3.3.4). A relative trigger counts from
// local midnight of that date, in every zone. The check loop fires the
// alarm at that moment, and not at the offset from midnight UTC.
func TestTodoAlarm_DateOnlyDueEveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		db, q := testutil.NewTestDB(t)
		ctx := context.Background()
		todos := todo.NewService(db, q)
		td, err := todos.Create(ctx, todo.CreateParams{
			CalendarID: 1, Summary: "Water plants", DueDate: "2026-10-24",
		})
		if err != nil {
			t.Fatalf("create todo: %v", err)
		}
		if err := todos.ReplaceAlarms(ctx, td.ID, []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT1H", Description: "water"},
		}); err != nil {
			t.Fatalf("replace alarms: %v", err)
		}
		svc := NewTodoService(db, q, todos)
		want := time.Date(2026, 10, 23, 23, 0, 0, 0, time.Local)

		early, err := svc.CheckTodos(ctx, want.Add(-time.Minute))
		if err != nil {
			t.Fatalf("CheckTodos: %v", err)
		}
		if len(early) != 0 {
			t.Fatalf("one minute before 23:00 local: got %d due alarms at %v, want 0", len(early), early[0].TriggerAt)
		}

		due, err := svc.CheckTodos(ctx, want.Add(time.Minute))
		if err != nil {
			t.Fatalf("CheckTodos: %v", err)
		}
		if len(due) != 1 || !due[0].TriggerAt.Equal(want) {
			t.Fatalf("due = %+v, want one alarm at %s", due, want)
		}
		if due[0].Todo.DueDate != "2026-10-24" {
			t.Fatalf("due todo DueDate = %q, want the date-only 2026-10-24", due[0].Todo.DueDate)
		}
	})
}

// TestEventAlarm_AllDayEveryZone guards issue #875 for an all-day event.
// The event starts at local midnight of its date, so a -PT15M trigger
// fires at 23:45 local on the day before, in every zone.
func TestEventAlarm_AllDayEveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		db, q := testutil.NewTestDB(t)
		ctx := context.Background()
		events := event.NewService(db, q)
		e, err := events.Create(ctx, event.CreateParams{
			CalendarID: 1, Title: "Holiday", AllDay: true,
			StartTime: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("create event: %v", err)
		}
		if err := events.ReplaceAlarms(ctx, e.ID, []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "holiday"},
		}); err != nil {
			t.Fatalf("replace alarms: %v", err)
		}
		svc := NewService(db, q, events, nil)
		want := time.Date(2026, 10, 27, 23, 45, 0, 0, time.Local)

		early, _, err := svc.Check(ctx, want.Add(-time.Minute))
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if len(early) != 0 {
			t.Fatalf("one minute before 23:45 local: got %d due alarms at %v, want 0", len(early), early[0].TriggerAt)
		}

		due, _, err := svc.Check(ctx, want.Add(time.Minute))
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if len(due) != 1 || !due[0].TriggerAt.Equal(want) {
			t.Fatalf("due = %+v, want one alarm at %s", due, want)
		}

		// Snooze until the start waits for local midnight of the date.
		stateID, err := svc.MarkFired(ctx, due[0])
		if err != nil {
			t.Fatalf("MarkFired: %v", err)
		}
		res, err := svc.SnoozeUntilStart(ctx, stateID, want.Add(time.Minute))
		if err != nil {
			t.Fatalf("SnoozeUntilStart: %v", err)
		}
		if start := time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local); !res.Until.Equal(start) {
			t.Fatalf("snooze until = %s, want %s", res.Until, start)
		}
	})
}
