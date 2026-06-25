package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/alarm"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// stubFireAlarm installs a counting stub over fireAlarmFn for the duration of
// the test and returns a pointer to the invocation count.
func stubFireAlarm(t *testing.T) *int {
	t.Helper()
	orig := fireAlarmFn
	t.Cleanup(func() { fireAlarmFn = orig })
	var calls int
	fireAlarmFn = func(alarm.DueAlarm, alarmExecutionPolicy) error {
		calls++
		return nil
	}
	return &calls
}

func newAlarmTestApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CHRONCAL_DB", filepath.Join(dir, "chroncal.db"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	a, err := app.New(filepath.Join(dir, "chroncal.db"))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// TestMarkAndFireEventAlarmSkipsFireOnDuplicateClaim is the regression test for
// issue #70: when two checkers overlap, both observe "no state" and both call
// MarkFired with the same (alarm_id, trigger_at). The first INSERT wins; the
// second hits the UNIQUE constraint. fireAlarm must NOT run for the loser, so
// the alarm fires exactly once.
func TestMarkAndFireEventAlarmSkipsFireOnDuplicateClaim(t *testing.T) {
	ctx := context.Background()
	a := newAlarmTestApp(t)
	calls := stubFireAlarm(t)

	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	start := time.Now().Add(time.Hour)
	evt, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: cal.ID,
		Title:      "Standup",
		StartTime:  start,
		EndTime:    start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := a.Events.ReplaceAlarms(ctx, evt.ID, []model.Alarm{{
		Action:       "DISPLAY",
		TriggerValue: "-PT15M",
		Description:  "Standup reminder",
		Related:      "START",
	}}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}
	alarms, err := a.Events.ListAlarms(ctx, evt.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("list alarms: %v (got %d)", err, len(alarms))
	}

	da := alarm.DueAlarm{
		Event:     evt,
		Alarm:     alarms[0],
		TriggerAt: start.Add(-15 * time.Minute).UTC(),
	}

	// First checker: claims and fires.
	if _, markErr, fireErr := markAndFireEventAlarm(ctx, a, da, alarmExecutionPolicy{}); markErr != nil || fireErr != nil {
		t.Fatalf("first claim: markErr=%v fireErr=%v", markErr, fireErr)
	}
	if *calls != 1 {
		t.Fatalf("after first claim: fireAlarm called %d times, want 1", *calls)
	}

	// Second (overlapping) checker: same DueAlarm, no state observed. The
	// MarkFired INSERT collides on (alarm_id, trigger_at).
	stateID, markErr, fireErr := markAndFireEventAlarm(ctx, a, da, alarmExecutionPolicy{})
	if markErr != nil {
		t.Fatalf("duplicate claim returned markErr=%v; a lost race must not count as a DB fault", markErr)
	}
	if fireErr != nil {
		t.Fatalf("duplicate claim returned fireErr=%v, want nil", fireErr)
	}
	if stateID != 0 {
		t.Fatalf("duplicate claim returned stateID=%d, want 0", stateID)
	}
	if *calls != 1 {
		t.Fatalf("duplicate claim fired the alarm: fireAlarm called %d times, want 1", *calls)
	}
}

// TestMarkAndFireTodoAlarmSkipsFireOnDuplicateClaim mirrors the event test for
// the todo variant.
func TestMarkAndFireTodoAlarmSkipsFireOnDuplicateClaim(t *testing.T) {
	ctx := context.Background()
	a := newAlarmTestApp(t)
	calls := stubFireAlarm(t)

	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	due := time.Now().Add(time.Hour)
	td, err := a.Todos.Create(ctx, todo.CreateParams{
		CalendarID: cal.ID,
		Summary:    "File report",
		DueDate:    due.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	if err := a.Todos.ReplaceAlarms(ctx, td.ID, []model.Alarm{{
		Action:       "DISPLAY",
		TriggerValue: "-PT15M",
		Description:  "Report reminder",
		Related:      "START",
	}}); err != nil {
		t.Fatalf("replace todo alarms: %v", err)
	}
	alarms, err := a.Todos.ListAlarms(ctx, td.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("list todo alarms: %v (got %d)", err, len(alarms))
	}

	tda := alarm.TodoDueAlarm{
		Todo:      td,
		Alarm:     alarms[0],
		TriggerAt: due.Add(-15 * time.Minute).UTC(),
	}

	if _, markErr, fireErr := markAndFireTodoAlarm(ctx, a, tda, alarmExecutionPolicy{}); markErr != nil || fireErr != nil {
		t.Fatalf("first claim: markErr=%v fireErr=%v", markErr, fireErr)
	}
	if *calls != 1 {
		t.Fatalf("after first claim: fireAlarm called %d times, want 1", *calls)
	}

	stateID, markErr, fireErr := markAndFireTodoAlarm(ctx, a, tda, alarmExecutionPolicy{})
	if markErr != nil {
		t.Fatalf("duplicate claim returned markErr=%v; a lost race must not count as a DB fault", markErr)
	}
	if fireErr != nil {
		t.Fatalf("duplicate claim returned fireErr=%v, want nil", fireErr)
	}
	if stateID != 0 {
		t.Fatalf("duplicate claim returned stateID=%d, want 0", stateID)
	}
	if *calls != 1 {
		t.Fatalf("duplicate claim fired the alarm: fireAlarm called %d times, want 1", *calls)
	}
}
