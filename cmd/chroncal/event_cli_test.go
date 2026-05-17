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
