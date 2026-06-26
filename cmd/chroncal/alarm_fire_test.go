package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
	a, _ := newAlarmTestAppWithPath(t)
	return a
}

// newAlarmTestAppWithPath builds a fresh app on a temp DB and returns both the
// app and the DB path, so a test can open a second connection on the same DB
// to model two concurrent checkers.
func newAlarmTestAppWithPath(t *testing.T) (*app.App, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chroncal.db")
	t.Setenv("CHRONCAL_DB", dbPath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a, dbPath
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
	if res := markAndFireEventAlarm(ctx, a, da, alarmExecutionPolicy{}); res.MarkErr != nil || res.FireErr != nil || !res.Fired {
		t.Fatalf("first claim: %+v, want Fired=true with no errors", res)
	}
	if *calls != 1 {
		t.Fatalf("after first claim: fireAlarm called %d times, want 1", *calls)
	}

	// Second (overlapping) checker: same DueAlarm, no state observed. The
	// MarkFired INSERT collides on (alarm_id, trigger_at).
	res := markAndFireEventAlarm(ctx, a, da, alarmExecutionPolicy{})
	if res.MarkErr != nil {
		t.Fatalf("duplicate claim returned MarkErr=%v; a lost race must not count as a DB fault", res.MarkErr)
	}
	if res.FireErr != nil {
		t.Fatalf("duplicate claim returned FireErr=%v, want nil", res.FireErr)
	}
	if res.Fired {
		t.Fatalf("duplicate claim reported Fired=true; the lost claim must signal not-fired")
	}
	if res.StateID != 0 {
		t.Fatalf("duplicate claim returned StateID=%d, want 0", res.StateID)
	}
	if *calls != 1 {
		t.Fatalf("duplicate claim fired the alarm: fireAlarm called %d times, want 1", *calls)
	}
}

// TestRunAlarmCheckEmitsNoFiredRecordOnLostClaim is the end-to-end regression
// for issue #70's review follow-up: a checker that loses the claim race must
// not emit a "fired" JSON record (which would carry state_id:0 and break
// scripts consuming -o json).
//
// It reproduces the Check-then-claim TOCTOU window deterministically: checker B
// runs runAlarmCheck and captures the due alarm in Check; the afterCheckForTest
// seam then lets a competing checker A claim and fire the same alarm before B
// reaches MarkFired. B therefore loses the UNIQUE (alarm_id, trigger_at) race
// at claim time and must emit no record.
func TestRunAlarmCheckEmitsNoFiredRecordOnLostClaim(t *testing.T) {
	ctx := context.Background()
	a, dbPath := newAlarmTestAppWithPath(t)

	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	// Trigger fires 15m before start; place start a minute ago so the alarm
	// is already due (trigger ~16m in the past, well inside StaleThreshold).
	start := time.Now().Add(-time.Minute)
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

	// Competing checker A on the same DB. Its mark-and-fire is driven from B's
	// after-Check seam so the claim lands in B's TOCTOU window.
	b, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New (checker B): %v", err)
	}
	defer b.Close()

	due, _, err := b.Alarms.Check(ctx, time.Now())
	if err != nil || len(due) != 1 {
		t.Fatalf("precondition Check: err=%v due=%d, want one due alarm", err, len(due))
	}
	aDueAlarm := due[0]

	calls := stubFireAlarm(t)

	var aClaimed bool
	prevHook := afterCheckForTest
	afterCheckForTest = func() {
		if aClaimed {
			return // claim exactly once, even if the seam runs again
		}
		aClaimed = true
		if res := markAndFireEventAlarm(ctx, a, aDueAlarm, alarmExecutionPolicy{}); !res.Fired {
			t.Errorf("competing checker A failed to claim: %+v", res)
		}
	}
	t.Cleanup(func() { afterCheckForTest = prevHook })

	prevFmt := outputFmt
	outputFmt = "json"
	t.Cleanup(func() { outputFmt = prevFmt })

	var bOut bytes.Buffer
	if err := runAlarmCheck(ctx, b, &bOut, time.Now(), alarmExecutionPolicy{}); err != nil {
		t.Fatalf("checker B runAlarmCheck: %v", err)
	}

	if *calls != 1 {
		t.Fatalf("alarm fired %d times across both checkers, want exactly 1", *calls)
	}

	var records []map[string]any
	if err := json.Unmarshal(bOut.Bytes(), &records); err != nil {
		t.Fatalf("parse checker B JSON %q: %v", bOut.String(), err)
	}
	if len(records) != 0 {
		t.Fatalf("checker B emitted %d record(s) on a lost claim, want 0: %s", len(records), bOut.String())
	}

	// The wire shape must be an empty JSON array, not null (issue #217). A
	// non-empty due set whose alarms all lose the claim race left results as a
	// nil slice, which json.Encode renders as `null` and breaks consumers
	// doing `jq '.[]'`. This must match the zero-due-set branch, which emits
	// `[]`. json.Unmarshal accepts both null and [], so assert the raw shape.
	if got := strings.TrimSpace(bOut.String()); got != "[]" {
		t.Fatalf("checker B JSON shape on all-claims-lost = %q, want %q", got, "[]")
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

	if res := markAndFireTodoAlarm(ctx, a, tda, alarmExecutionPolicy{}); res.MarkErr != nil || res.FireErr != nil || !res.Fired {
		t.Fatalf("first claim: %+v, want Fired=true with no errors", res)
	}
	if *calls != 1 {
		t.Fatalf("after first claim: fireAlarm called %d times, want 1", *calls)
	}

	res := markAndFireTodoAlarm(ctx, a, tda, alarmExecutionPolicy{})
	if res.MarkErr != nil {
		t.Fatalf("duplicate claim returned MarkErr=%v; a lost race must not count as a DB fault", res.MarkErr)
	}
	if res.FireErr != nil {
		t.Fatalf("duplicate claim returned FireErr=%v, want nil", res.FireErr)
	}
	if res.Fired {
		t.Fatalf("duplicate claim reported Fired=true; the lost claim must signal not-fired")
	}
	if res.StateID != 0 {
		t.Fatalf("duplicate claim returned StateID=%d, want 0", res.StateID)
	}
	if *calls != 1 {
		t.Fatalf("duplicate claim fired the alarm: fireAlarm called %d times, want 1", *calls)
	}
}
