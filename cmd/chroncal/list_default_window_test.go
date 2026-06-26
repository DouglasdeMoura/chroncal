package main

import (
	"strings"
	"testing"
)

// TestTodoListDefaultWindowShowsOverdue verifies that `todo list` with no date
// flags surfaces overdue (past-due) todos. Regression test for issue #304: the
// default window anchored its lower bound to today, silently hiding every
// incomplete todo whose due date was in the past — exactly the items the user
// most needs to see.
func TestTodoListDefaultWindowShowsOverdue(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	if _, _, err := runChroncalCommand(t,
		"todo", "add", "Overdue task",
		"--calendar", "Work",
		"--due", "2020-01-01",
	); err != nil {
		t.Fatalf("todo add: %v", err)
	}

	// No --from/--to: the default window must still include the overdue todo.
	stdout, _, err := runChroncalCommand(t, "todo", "list", "--compact")
	if err != nil {
		t.Fatalf("todo list: %v", err)
	}
	if !strings.Contains(stdout, "Overdue task") {
		t.Fatalf("todo list (no flags) = %q, want it to contain overdue todo %q", stdout, "Overdue task")
	}
}

// TestJournalListDefaultWindowShowsPast verifies that `journal list` with no
// date flags surfaces past-dated entries. Regression test for issue #304:
// journal entries are inherently retrospective, but the forward-only default
// window hid essentially every real entry.
func TestJournalListDefaultWindowShowsPast(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	if _, _, err := runChroncalCommand(t,
		"journal", "add", "Past entry",
		"--calendar", "Work",
		"--date", "2020-01-01",
		"--status", "FINAL",
	); err != nil {
		t.Fatalf("journal add: %v", err)
	}

	// No --from/--to: the default window must still include the past entry.
	stdout, _, err := runChroncalCommand(t, "journal", "list", "--compact")
	if err != nil {
		t.Fatalf("journal list: %v", err)
	}
	if !strings.Contains(stdout, "Past entry") {
		t.Fatalf("journal list (no flags) = %q, want it to contain past entry %q", stdout, "Past entry")
	}
}
