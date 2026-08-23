package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestJournalListHidesCancelledByDefault verifies that `journal list` hides
// CANCELLED entries by default and that `--all` brings them back. That
// matches how `todo list` hides COMPLETED/CANCELLED. Regression test for
// issue #136, where `--all` was a dead flag and CANCELLED journals were
// always shown.
func TestJournalListHidesCancelledByDefault(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	if _, _, err := runChroncalCommand(t,
		"journal", "add", "Active note",
		"--calendar", "Work",
		"--date", "2026-04-10",
		"--status", "FINAL",
	); err != nil {
		t.Fatalf("journal add final: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"journal", "add", "Scrapped note",
		"--calendar", "Work",
		"--date", "2026-04-10",
		"--status", "CANCELLED",
	); err != nil {
		t.Fatalf("journal add cancelled: %v", err)
	}

	listArgs := []string{
		"journal", "list",
		"--calendar", "Work",
		"--from", "2026-04-01",
		"--to", "2026-04-30",
		"--compact",
	}

	// Default: CANCELLED entry must be hidden.
	stdout, _, err := runChroncalCommand(t, listArgs...)
	if err != nil {
		t.Fatalf("journal list: %v", err)
	}
	if !strings.Contains(stdout, "Active note") {
		t.Fatalf("default list = %q, want it to contain %q", stdout, "Active note")
	}
	if strings.Contains(stdout, "Scrapped note") {
		t.Fatalf("default list = %q, should hide CANCELLED entry %q", stdout, "Scrapped note")
	}

	// --all: CANCELLED entry must reappear.
	stdoutAll, _, err := runChroncalCommand(t, append(listArgs, "--all")...)
	if err != nil {
		t.Fatalf("journal list --all: %v", err)
	}
	if !strings.Contains(stdoutAll, "Active note") {
		t.Fatalf("--all list = %q, want it to contain %q", stdoutAll, "Active note")
	}
	if !strings.Contains(stdoutAll, "Scrapped note") {
		t.Fatalf("--all list = %q, want it to contain CANCELLED entry %q", stdoutAll, "Scrapped note")
	}

	// --status CANCELLED: explicit status filter shows only cancelled.
	stdoutStatus, _, err := runChroncalCommand(t, append(listArgs, "--status", "CANCELLED")...)
	if err != nil {
		t.Fatalf("journal list --status CANCELLED: %v", err)
	}
	if !strings.Contains(stdoutStatus, "Scrapped note") {
		t.Fatalf("--status CANCELLED list = %q, want it to contain %q", stdoutStatus, "Scrapped note")
	}
	if strings.Contains(stdoutStatus, "Active note") {
		t.Fatalf("--status CANCELLED list = %q, should not contain FINAL entry %q", stdoutStatus, "Active note")
	}
}

// TestJournalExdateParseFailureIsInvalidInput guards the error taxonomy. A
// bad --exdate value in journal add/update used to surface as a generic
// error. It must report invalid_input like the event and todo commands.
func TestJournalExdateParseFailureIsInvalidInput(t *testing.T) {
	setupCalendarCLITestEnv(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	_, stderr, err := runChroncalCommand(t, "journal", "add", "Note",
		"--calendar", "Work", "--exdate", "not-a-date", "--output", "json")
	if err == nil {
		t.Fatal("journal add accepted a bad --exdate value")
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode error payload %q: %v", stderr, jerr)
	}
	if payload.Code != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input", payload.Code)
	}

	if _, _, err := runChroncalCommand(t, "journal", "add", "Note", "--calendar", "Work"); err != nil {
		t.Fatalf("journal add: %v", err)
	}
	_, stderr, err = runChroncalCommand(t, "journal", "update", "1",
		"--exdate", "not-a-date", "--output", "json")
	if err == nil {
		t.Fatal("journal update accepted a bad --exdate value")
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode error payload %q: %v", stderr, jerr)
	}
	if payload.Code != "invalid_input" {
		t.Fatalf("journal update: code = %q, want invalid_input", payload.Code)
	}
}
