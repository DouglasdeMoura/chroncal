package main

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// TestEventUpdateAlarmKeepsSyncOnlyRows guards the issue #579 CLI gap.
// The --alarm flag replaces the full alarm set, and its syntax cannot
// state a preserved non-fireable action. The update command must carry
// the stored sync-only rows forward. Without the carry-over, the next
// push deletes the VALARM of another client from the server.
func TestEventUpdateAlarmKeepsSyncOnlyRows(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
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

	// Seed a preserved sync-only alarm the way a sync pull does. The CLI
	// parser cannot state this action, so the seed goes through the
	// service.
	seed, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New (seed): %v", err)
	}
	if err := seed.Events.ReplaceAlarms(ctx, 1, []model.Alarm{
		{Action: "NONE", TriggerValue: "19760401T005545Z", UID: "google-none@test"},
	}); err != nil {
		seed.Close()
		t.Fatalf("seed sync-only alarm: %v", err)
	}
	seed.Close()

	if _, _, err := runChroncalCommand(t,
		"event", "update", "1", "--alarm", "-PT30M",
	); err != nil {
		t.Fatalf("event update --alarm: %v", err)
	}

	check, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New (check): %v", err)
	}
	defer check.Close()
	alarms, err := check.Events.ListAlarms(ctx, 1)
	if err != nil {
		t.Fatalf("list alarms: %v", err)
	}
	if len(alarms) != 2 {
		t.Fatalf("alarms = %d, want 2 (the new DISPLAY and the preserved NONE); got %+v", len(alarms), alarms)
	}
	var sawNone, sawDisplay bool
	for _, a := range alarms {
		switch a.Action {
		case "NONE":
			sawNone = true
		case "DISPLAY":
			sawDisplay = true
		}
	}
	if !sawNone {
		t.Errorf("the preserved NONE alarm is gone; alarms = %+v", alarms)
	}
	if !sawDisplay {
		t.Errorf("the new DISPLAY alarm is missing; alarms = %+v", alarms)
	}
}
