package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventListVerboseUsesTimeRailView(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	if _, _, err := runChroncalCommand(t,
		"event", "add", "Team Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
		"--location", "Zoom",
		"--description", "Sprint planning",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--verbose",
		"--show-id",
		"--show-calendar",
		"--calendar", "Work",
		"--from", "2026-04-21",
		"--to", "2026-04-22",
		"--show-weekday",
	)
	if err != nil {
		t.Fatalf("event list --verbose: %v", err)
	}

	wantPrefix := "" +
		"Apr 21 Tue\n" +
		"----------\n" +
		"09:00   | Team Standup ("
	if !strings.HasPrefix(stdout, wantPrefix) {
		t.Fatalf("event list --verbose output mismatch\nwant prefix:\n%s\ngot:\n%s", wantPrefix, stdout)
	}
	for _, needle := range []string{"        | Zoom\n", "        | Sprint planning\n", "Calendar: Work"} {
		if !strings.Contains(stdout, needle) {
			t.Fatalf("event list --verbose output = %q, want substring %q", stdout, needle)
		}
	}
	if strings.Contains(stdout, "uid: ") {
		t.Fatalf("event list --verbose output = %q, should not show uid", stdout)
	}
}

func TestEventListCompactCanShowEventIDAndCalendar(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	if _, _, err := runChroncalCommand(t,
		"event", "add", "Team Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--show-id",
		"--show-calendar",
		"--calendar", "Work",
		"--from", "2026-04-21",
		"--to", "2026-04-22",
		"--show-weekday",
	)
	if err != nil {
		t.Fatalf("event list compact flags: %v", err)
	}

	for _, needle := range []string{"Team Standup (", "[Work]"} {
		if !strings.Contains(stdout, needle) {
			t.Fatalf("event list output = %q, want substring %q", stdout, needle)
		}
	}
}

// TestEventAddAndUpdateShareDetailBlock locks in that event add and event
// update emit the same detail-block shape used by event get, so the
// CLI doesn't have one prose summary on create and a structured block
// on update.
func TestEventAddAndUpdateShareDetailBlock(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	addOut, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
	)
	if err != nil {
		t.Fatalf("event add: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(addOut), "Created:") {
		t.Fatalf("event add output starts with 'Created:' prose; want the same detail block as event get:\n%s", addOut)
	}
	for _, needle := range []string{"  Standup\n", "    when:", "    id:", "    uid:"} {
		if !strings.Contains(addOut, needle) {
			t.Fatalf("event add output = %q, missing %q", addOut, needle)
		}
	}

	updateOut, _, err := runChroncalCommand(t,
		"event", "update", "1",
		"--title", "Daily Standup",
	)
	if err != nil {
		t.Fatalf("event update: %v", err)
	}
	for _, needle := range []string{"  Daily Standup\n", "    when:", "    id:", "    uid:"} {
		if !strings.Contains(updateOut, needle) {
			t.Fatalf("event update output = %q, missing %q", updateOut, needle)
		}
	}
}

// TestNotFoundErrorHasNoWrapPrefix locks in that user-facing error
// messages don't leak the internal fmt.Errorf wrap chain (e.g.
// "get event: event 999 not found"). printCLIError prefers the
// *cliError.Msg over the outer wrapped message.
func TestNotFoundErrorHasNoWrapPrefix(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, _, err := runChroncalCommand(t, "event", "get", "999")
	if err == nil {
		t.Fatal("event get 999 should fail")
	}
	got := err.Error()
	if !strings.Contains(got, "event 999 not found") {
		t.Fatalf("error = %q, want it to contain %q", got, "event 999 not found")
	}
	if strings.Contains(got, "get event:") {
		t.Fatalf("error = %q, should not leak the 'get event:' wrap prefix", got)
	}
}

// TestEventJSONTimestampsAreUTC locks in the documented policy that
// --output json emits RFC 3339 in UTC (Z suffix) regardless of the
// terminal's TZ. Text mode keeps local time and is covered elsewhere.
func TestEventJSONTimestampsAreUTC(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "America/New_York")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--from", "2026-04-21",
		"--to", "2026-04-21",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list --output json: %v", err)
	}

	var events []struct {
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &events); jerr != nil {
		t.Fatalf("decode %q: %v", stdout, jerr)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	for _, ts := range []string{e.StartTime, e.EndTime, e.CreatedAt, e.UpdatedAt} {
		if !strings.HasSuffix(ts, "Z") {
			t.Fatalf("timestamp %q is not UTC (no Z suffix); JSON output must be in UTC", ts)
		}
	}
	// 09:00 America/New_York on 2026-04-21 is 13:00 UTC (EDT, UTC-4).
	if e.StartTime != "2026-04-21T13:00:00Z" {
		t.Fatalf("start_time = %q, want %q (09:00 EDT = 13:00 UTC)", e.StartTime, "2026-04-21T13:00:00Z")
	}
}

func TestNotFoundErrorJSONHasNoWrapPrefix(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t, "event", "get", "999", "--output", "json")
	if err == nil {
		t.Fatal("event get 999 --output json should fail")
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode error payload %q: %v", stderr, jerr)
	}
	if payload.Code != "not_found" {
		t.Fatalf("code = %q, want %q", payload.Code, "not_found")
	}
	if payload.Error != "event 999 not found" {
		t.Fatalf("error = %q, want %q (no wrap prefix)", payload.Error, "event 999 not found")
	}
}
