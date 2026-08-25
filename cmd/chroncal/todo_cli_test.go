package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/todo"
)

func TestWriteCompactTodoTableLayout(t *testing.T) {
	t.Parallel()

	todos := []todo.Todo{
		{ID: 7, Summary: "Pay invoice", DueDate: "2026-08-12",
			Status: "COMPLETED", Categories: "home,urgent"},
		{ID: 9, Summary: "Read Go spec"},
	}

	var b bytes.Buffer
	writeCompactTodoTable(&b, todos, true, false)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if got := strings.Fields(lines[0]); !reflect.DeepEqual(got,
		[]string{"ID", "STATE", "DUE", "CATEGORIES", "SUMMARY"}) {
		t.Fatalf("header fields = %v", got)
	}
	col := strings.Index(lines[0], "SUMMARY")
	wantSummaries := []string{"Pay invoice", "Read Go spec"}
	for i, line := range lines[1:] {
		if got := strings.Index(line, wantSummaries[i]); got != col {
			t.Fatalf("row %d summary at %d, want %d: %q", i, got, col, line)
		}
	}
	if !strings.Contains(lines[1], "[x]") {
		t.Fatalf("row 1 = %q, want completed checkbox", lines[1])
	}

	// Completed todos render green; open ones render dim.
	b.Reset()
	writeCompactTodoTable(&b, todos, false, true)
	if !strings.Contains(b.String(), "\x1b[32m") {
		t.Fatalf("table = %q, want green for the completed todo", b.String())
	}

	// The categories column disappears when no todo carries one.
	b.Reset()
	writeCompactTodoTable(&b, []todo.Todo{{ID: 1, Summary: "Plain"}}, true, false)
	if strings.Contains(b.String(), "CATEGORIES") {
		t.Fatalf("table = %q, want no categories column", b.String())
	}
}

// TestTodoSearchCompletedAndIncompleteAreMutuallyExclusive reproduces issue
// #361. Both --completed and --incomplete on `todo search` were
// accepted in silence. The second flag was ignored rather than an error
// being returned. The user then saw results that mislead.
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

// TestTodoExdateParseFailureIsInvalidInput guards the error taxonomy. A bad
// --exdate value in todo add/update used to surface as a generic error.
// The event commands already tagged it invalid_input; the todo commands
// must report the same code so --output json consumers can dispatch on it.
func TestTodoExdateParseFailureIsInvalidInput(t *testing.T) {
	setupCalendarCLITestEnv(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	assertInvalidInput := func(args ...string) {
		t.Helper()
		_, stderr, err := runChroncalCommand(t, args...)
		if err == nil {
			t.Fatalf("%s accepted a bad --exdate value", strings.Join(args, " "))
		}
		var payload struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
			t.Fatalf("%s: decode error payload %q: %v", strings.Join(args, " "), stderr, jerr)
		}
		if payload.Code != "invalid_input" {
			t.Fatalf("%s: code = %q, want invalid_input", strings.Join(args, " "), payload.Code)
		}
	}

	assertInvalidInput("todo", "add", "Task", "--calendar", "Work",
		"--exdate", "not-a-date", "--output", "json")

	if _, _, err := runChroncalCommand(t, "todo", "add", "Task", "--calendar", "Work"); err != nil {
		t.Fatalf("todo add: %v", err)
	}
	assertInvalidInput("todo", "update", "1",
		"--recurrence-date-times", "not-a-date", "--output", "json")
}
