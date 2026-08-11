package main

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/journal"
)

func TestFormatCompactJournalIncludesIDAndCategories(t *testing.T) {
	t.Parallel()

	gotHeader := formatCompactJournalHeader(false)
	wantHeader := "ID    DATE        CATEGORIES          SUMMARY"
	if gotHeader != wantHeader {
		t.Fatalf("formatCompactJournalHeader() = %q, want %q", gotHeader, wantHeader)
	}

	entry := journal.Journal{
		ID:         19,
		StartDate:  "2026-08-11",
		Summary:    "Journal ID check",
		Categories: "work, personal",
	}
	got := formatCompactJournal(entry, false)
	want := "19    2026-08-11  work, personal      Journal ID check"
	if got != want {
		t.Fatalf("formatCompactJournal() = %q, want %q", got, want)
	}

	coloredHeader := formatCompactJournalHeader(true)
	coloredRow := formatCompactJournal(entry, true)
	if !strings.Contains(coloredHeader, "\x1b[1;36m") {
		t.Fatalf("colored header = %q, want bold cyan ANSI styling", coloredHeader)
	}
	for _, code := range []string{"\x1b[1;36m", "\x1b[2m", "\x1b[33m"} {
		if !strings.Contains(coloredRow, code) {
			t.Fatalf("colored row = %q, want ANSI code %q", coloredRow, code)
		}
	}
}

func TestJournalListCompactIDCanBePassedToGet(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"journal", "add", "Sprint notes",
		"--calendar", "Work",
		"--date", "2026-08-11",
		"--categories", "work,personal",
	); err != nil {
		t.Fatalf("journal add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"journal", "list", "--compact",
		"--calendar", "Work",
		"--from", "2026-08-11",
		"--to", "2026-08-12",
	)
	if err != nil {
		t.Fatalf("journal list --compact: %v", err)
	}
	if !strings.Contains(stdout, "ID    DATE        CATEGORIES          SUMMARY\n") {
		t.Fatalf("compact output = %q, want table header", stdout)
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Fatalf("piped compact output contains ANSI styling: %q", stdout)
	}
	if !strings.Contains(stdout, "personal,work       Sprint notes") {
		t.Fatalf("compact output = %q, want summary and categories on one line", stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) < 2 {
		t.Fatalf("compact output contains no data row: %q", stdout)
	}
	fields := strings.Fields(lines[1])
	if len(fields) == 0 {
		t.Fatalf("compact output contains no ID: %q", stdout)
	}

	getOut, _, err := runChroncalCommand(t, "journal", "get", fields[0])
	if err != nil {
		t.Fatalf("journal get %q: %v", fields[0], err)
	}
	if !strings.Contains(getOut, "Sprint notes") {
		t.Fatalf("journal get output = %q, want summary %q", getOut, "Sprint notes")
	}
}

// TestJournalListHidesCancelledByDefault verifies that `journal list` hides
// CANCELLED entries by default and that `--all` brings them back, mirroring
// how `todo list` hides COMPLETED/CANCELLED. Regression test for issue #136,
// where `--all` was a dead flag and CANCELLED journals were always shown.
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
