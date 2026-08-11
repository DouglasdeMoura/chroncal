package main

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/todo"
)

func TestFormatCompactTodoUsesSharedTableLayout(t *testing.T) {
	t.Parallel()

	td := todo.Todo{
		ID:         7,
		Summary:    "Pay invoice",
		DueDate:    "2026-08-12",
		Status:     "COMPLETED",
		Categories: "home,urgent",
	}
	header := formatCompactTodoHeader(false)
	if got, want := strings.Fields(header), []string{"ID", "STATE", "DUE", "CATEGORIES", "SUMMARY"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("todo compact header fields = %v, want %v", got, want)
	}
	got := formatCompactTodo(td, false)
	want := "7     [x]    2026-08-12  home,urgent         Pay invoice"
	if got != want {
		t.Fatalf("formatCompactTodo() = %q, want %q", got, want)
	}
	colored := formatCompactTodo(td, true)
	for _, code := range []string{"\x1b[1;36m", "\x1b[32m", "\x1b[2m", "\x1b[33m"} {
		if !strings.Contains(colored, code) {
			t.Fatalf("colored todo row = %q, want ANSI code %q", colored, code)
		}
	}
}

func TestTodoListCompactIDCanBePassedToGet(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"todo", "add", "Pay invoice",
		"--calendar", "Work",
		"--due", "2026-08-12",
		"--categories", "home,urgent",
	); err != nil {
		t.Fatalf("todo add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"todo", "list", "--compact",
		"--calendar", "Work",
		"--from", "2026-08-11",
		"--to", "2026-08-13",
	)
	if err != nil {
		t.Fatalf("todo list --compact: %v", err)
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Fatalf("piped compact output contains ANSI styling: %q", stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) < 2 {
		t.Fatalf("compact output contains no data row: %q", stdout)
	}
	if !strings.Contains(lines[0], "STATE") || !strings.Contains(lines[0], "CATEGORIES") ||
		!strings.Contains(lines[1], "home,urgent") {
		t.Fatalf("compact todo table missing expected columns: %q", stdout)
	}
	id := strings.Fields(lines[1])[0]
	getOut, _, err := runChroncalCommand(t, "todo", "get", id)
	if err != nil {
		t.Fatalf("todo get %q: %v", id, err)
	}
	if !strings.Contains(getOut, "Pay invoice") {
		t.Fatalf("todo get output = %q, want summary", getOut)
	}
}

// TestTodoSearchCompletedAndIncompleteAreMutuallyExclusive reproduces issue
// #361: passing both --completed and --incomplete to `todo search` was
// silently accepted. The second flag was ignored rather than an error being
// returned, leaving the user with misleading results.
func TestTodoSearchCompletedAndIncompleteAreMutuallyExclusive(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, _, err := runChroncalCommand(t,
		"todo", "search", "anything",
		"--completed", "--incomplete",
	)
	if err == nil {
		t.Fatal("todo search --completed --incomplete: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("todo search --completed --incomplete: error = %q, want it to mention %q",
			err.Error(), "mutually exclusive")
	}
}
