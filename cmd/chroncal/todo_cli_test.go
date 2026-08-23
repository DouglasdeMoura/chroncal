package main

import (
	"encoding/json"
	"strings"
	"testing"
)

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
