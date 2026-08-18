package main

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// seedEventWithForeignAlarm creates one event that carries a preserved
// sync-only alarm and one alarm the CLI can state. A sync pull writes such
// a pair, and the CLI parser cannot state the sync-only action, so the seed
// goes through the service.
func seedEventWithForeignAlarm(t *testing.T, dbPath string) {
	t.Helper()
	ctx := context.Background()

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Synced meeting",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	seed, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New (seed): %v", err)
	}
	defer seed.Close()
	if err := seed.Events.ReplaceAlarms(ctx, 1, []model.Alarm{
		{Action: "NONE", TriggerValue: "19760401T005545Z", UID: "google-none@test"},
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	}); err != nil {
		t.Fatalf("seed alarms: %v", err)
	}
}

// eventAlarmActions lists the stored actions of event 1.
func eventAlarmActions(t *testing.T, dbPath string) []string {
	t.Helper()
	check, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New (check): %v", err)
	}
	defer check.Close()
	alarms, err := check.Events.ListAlarms(context.Background(), 1)
	if err != nil {
		t.Fatalf("list alarms: %v", err)
	}
	actions := make([]string, 0, len(alarms))
	for _, a := range alarms {
		actions = append(actions, a.Action)
	}
	return actions
}

// TestEventUpdateClearForeignAlarmsWithAlarmFlag pins the escape from the
// carry-over rule (issue #593). The flag makes the --alarm set the whole
// set, so the preserved row goes with the rows the flag replaces.
func TestEventUpdateClearForeignAlarmsWithAlarmFlag(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedEventWithForeignAlarm(t, dbPath)

	if _, _, err := runChroncalCommand(t,
		"event", "update", "1", "--alarm", "-PT30M", "--clear-foreign-alarms",
	); err != nil {
		t.Fatalf("event update --alarm --clear-foreign-alarms: %v", err)
	}

	actions := eventAlarmActions(t, dbPath)
	if len(actions) != 1 || actions[0] != "DISPLAY" {
		t.Fatalf("actions = %v, want one DISPLAY alarm and no preserved row", actions)
	}
}

// TestEventUpdateClearForeignAlarmsAlone pins the flag on its own. It
// removes the preserved rows and keeps the alarms the user can state.
func TestEventUpdateClearForeignAlarmsAlone(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedEventWithForeignAlarm(t, dbPath)

	if _, _, err := runChroncalCommand(t,
		"event", "update", "1", "--clear-foreign-alarms",
	); err != nil {
		t.Fatalf("event update --clear-foreign-alarms: %v", err)
	}

	actions := eventAlarmActions(t, dbPath)
	if len(actions) != 1 || actions[0] != "DISPLAY" {
		t.Fatalf("actions = %v, want the stored DISPLAY alarm to stay and the preserved row to go", actions)
	}
}

// TestEventUpdateWithoutFlagKeepsForeignAlarm pins the default. An update
// that does not pass the flag keeps the preserved row.
func TestEventUpdateWithoutFlagKeepsForeignAlarm(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedEventWithForeignAlarm(t, dbPath)

	if _, _, err := runChroncalCommand(t,
		"event", "update", "1", "--title", "Renamed",
	); err != nil {
		t.Fatalf("event update --title: %v", err)
	}

	actions := eventAlarmActions(t, dbPath)
	if len(actions) != 2 {
		t.Fatalf("actions = %v, want both alarms to stay", actions)
	}
}

// TestTodoUpdateClearForeignAlarms pins the todo twin of the flag.
func TestTodoUpdateClearForeignAlarms(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"todo", "add", "Synced task", "--calendar", "Work",
	); err != nil {
		t.Fatalf("todo add: %v", err)
	}

	seed, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New (seed): %v", err)
	}
	if err := seed.Todos.ReplaceAlarms(ctx, 1, []model.Alarm{
		{Action: "X-APPLE-SOUND", TriggerValue: "-PT5M", UID: "apple-sound@test"},
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	}); err != nil {
		seed.Close()
		t.Fatalf("seed alarms: %v", err)
	}
	seed.Close()

	if _, _, err := runChroncalCommand(t,
		"todo", "update", "1", "--clear-foreign-alarms",
	); err != nil {
		t.Fatalf("todo update --clear-foreign-alarms: %v", err)
	}

	check, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New (check): %v", err)
	}
	defer check.Close()
	alarms, err := check.Todos.ListAlarms(ctx, 1)
	if err != nil {
		t.Fatalf("list alarms: %v", err)
	}
	if len(alarms) != 1 || alarms[0].Action != "DISPLAY" {
		t.Fatalf("alarms = %+v, want only the DISPLAY alarm", alarms)
	}
}
