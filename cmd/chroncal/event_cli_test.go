package main

import (
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
	for _, needle := range []string{"        | Zoom\n", "        | Sprint planning\n"} {
		if !strings.Contains(stdout, needle) {
			t.Fatalf("event list --verbose output = %q, want substring %q", stdout, needle)
		}
	}
	for _, needle := range []string{"Team Standup (", "Calendar: Work"} {
		if !strings.Contains(stdout, needle) {
			t.Fatalf("event list --verbose output = %q, want substring %q", stdout, needle)
		}
	}
	if strings.Contains(stdout, "uid: ") {
		t.Fatalf("event list --verbose output = %q, should not show uid", stdout)
	}
}
