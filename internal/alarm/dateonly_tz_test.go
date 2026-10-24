package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
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

// TestSnooze_AllDayEveryZone guards issue #875 for a snooze by duration.
// An all-day event ends at local midnight after its last date, so a
// snooze in the evening of the day before is not "already ended".
func TestSnooze_AllDayEveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		evt := event.Event{
			Title: "Holiday", AllDay: true,
			StartTime: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		}
		now := time.Date(2026, 10, 28, 23, 0, 0, 0, time.Local)
		res, err := computeSnoozeResult(evt, 2*time.Hour, now)
		if err != nil {
			t.Fatalf("computeSnoozeResult: %v", err)
		}
		end := time.Date(2026, 10, 29, 0, 0, 0, 0, time.Local)
		if !res.Capped || !res.Until.Equal(end) || !res.PastStart {
			t.Fatalf("snooze = %+v, want capped at %s and past the start", res, end)
		}
		before := time.Date(2026, 10, 27, 22, 0, 0, 0, time.Local)
		res, err = computeSnoozeResult(evt, 10*time.Minute, before)
		if err != nil {
			t.Fatalf("computeSnoozeResult: %v", err)
		}
		if res.Capped || res.PastStart {
			t.Fatalf("snooze on the day before = %+v, want not capped and not past the start", res)
		}
	})
}

// TestTodoAlarm_InstanceKeepsDueForm checks the todo of a due alarm. A
// todo with a date-only DTSTART and a timed DUE keeps the timed DUE.
func TestTodoAlarm_InstanceKeepsDueForm(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		db, q := testutil.NewTestDB(t)
		ctx := context.Background()
		todos := todo.NewService(db, q)
		td, err := todos.Create(ctx, todo.CreateParams{
			CalendarID: 1, Summary: "Report", StartDate: "2026-10-24", DueDate: "2026-10-26T17:00:00Z",
		})
		if err != nil {
			t.Fatalf("create todo: %v", err)
		}
		if err := todos.ReplaceAlarms(ctx, td.ID, []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "PT1H", Description: "start"},
		}); err != nil {
			t.Fatalf("replace alarms: %v", err)
		}
		due, err := NewTodoService(db, q, todos).CheckTodos(ctx, time.Date(2026, 10, 24, 1, 30, 0, 0, time.Local))
		if err != nil {
			t.Fatalf("CheckTodos: %v", err)
		}
		if len(due) != 1 {
			t.Fatalf("due = %+v, want one alarm", due)
		}
		if due[0].Todo.DueDate != "2026-10-26T17:00:00Z" || due[0].Todo.StartDate != "2026-10-24" {
			t.Fatalf("due todo = DUE %q START %q, want the stored values", due[0].Todo.DueDate, due[0].Todo.StartDate)
		}
	})
}

// TestTodoTrigger_MixedEndEveryZone checks a RELATED=END trigger of a todo
// with a timed DTSTART and a date-only DUE. The end mixes the two forms,
// so the trigger keeps the instant reading. It does not move by the zone
// offset.
func TestTodoTrigger_MixedEndEveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		start := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
		inst := recurrence.ExpandedTodo{
			Todo:         todo.Todo{StartDate: start.Format(time.RFC3339), DueDate: "2026-10-29"},
			InstanceTime: start,
		}
		got, err := computeTodoTriggerTimeForInstance(inst, model.Alarm{TriggerValue: "-PT1H", Related: "END"})
		if err != nil {
			t.Fatalf("computeTodoTriggerTimeForInstance: %v", err)
		}
		if want := time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC); !got.Equal(want) {
			t.Fatalf("trigger = %s, want %s", got, want)
		}
	})
}
