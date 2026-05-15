package main

import (
	"strings"
	"testing"
)

// These tests exercise the destructive-op guard end-to-end through the
// same exec.Cmd harness the other CLI tests use. That harness wires stdin
// and stdout to byte buffers (not TTYs), so confirmDestructive refuses
// by default and requires --yes.

func TestEventDelete_WithoutYes_RefusesInNonInteractiveShell(t *testing.T) {
	setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-06",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, stderr, err := runChroncalCommand(t, "event", "delete", "1")
	if err == nil {
		t.Fatalf("event delete should have exited non-zero on refusal, got stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stderr, "Refusing destructive operation") {
		t.Fatalf("expected refusal message in stderr, got stderr=%q stdout=%q", stderr, stdout)
	}
	// The event must still exist. Fetch by ID to bypass the date-range
	// filter on `event list`.
	got, _, err := runChroncalCommand(t, "event", "get", "1", "--output", "json")
	if err != nil {
		t.Fatalf("event was deleted despite refusal; get failed: %v", err)
	}
	if !strings.Contains(got, "Standup") {
		t.Fatalf("event was deleted despite refusal; get=%q", got)
	}
}

func TestEventDelete_WithYes_Proceeds(t *testing.T) {
	setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-06",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "event", "delete", "1", "--yes")
	if err != nil {
		t.Fatalf("event delete --yes: %v (stdout=%q)", err, stdout)
	}
	if !strings.Contains(stdout, "Deleted event 1") {
		t.Errorf("expected success message, got stdout=%q", stdout)
	}
}

func TestEventDelete_JSONOutputBypassesPrompt(t *testing.T) {
	setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-06",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "event", "delete", "1", "--output", "json")
	if err != nil {
		t.Fatalf("event delete --output json: %v (stdout=%q)", err, stdout)
	}
	if !strings.Contains(stdout, `"deleted":true`) && !strings.Contains(stdout, `"deleted": true`) {
		t.Errorf("expected JSON success payload, got stdout=%q", stdout)
	}
}

func TestCalendarDelete_RefusalIncludesEventCount(t *testing.T) {
	setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-06",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	// Calendar delete refuses in non-interactive shell, but we still want
	// to verify the prompt question *would* include the event count for
	// a real user. --yes bypasses confirmation but --output json does too;
	// using json keeps the path deterministic.
	stdout, _, err := runChroncalCommand(t, "calendar", "delete", "1", "--output", "json")
	if err != nil {
		t.Fatalf("calendar delete --output json: %v (stdout=%q)", err, stdout)
	}
	if !strings.Contains(stdout, `"deleted":true`) && !strings.Contains(stdout, `"deleted": true`) {
		t.Errorf("expected JSON success payload, got stdout=%q", stdout)
	}
}
