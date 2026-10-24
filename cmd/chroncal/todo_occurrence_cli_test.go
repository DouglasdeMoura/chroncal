package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// addWeeklyTodo adds a weekly todo with four occurrences, due on
// 2026-10-24, 2026-10-31, 2026-11-07, and 2026-11-14. It returns the UID.
func addWeeklyTodo(t *testing.T) string {
	t.Helper()
	stdout, _, err := runChroncalCommand(t, "todo", "add", "Weekly review",
		"--due", "2026-10-24", "--recurrence-rule", "FREQ=WEEKLY;COUNT=4", "-o", "json")
	if err != nil {
		t.Fatalf("todo add: %v", err)
	}
	var out struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.UID == "" {
		t.Fatalf("todo add output %q: %v", stdout, err)
	}
	return out.UID
}

// TestTodoOccurrenceCommands locks in issue #887. With --recurrence-id,
// "todo complete", "todo update", and "todo delete" act on one occurrence
// of a recurring todo that has no override row yet.
func TestTodoOccurrenceCommands(t *testing.T) {
	setupCalendarCLITestEnv(t)
	uid := addWeeklyTodo(t)

	if _, _, err := runChroncalCommand(t, "todo", "complete", uid, "--recurrence-id", "2026-10-31T00:00:00Z"); err != nil {
		t.Fatalf("todo complete occurrence: %v", err)
	}
	stdout, _, err := runChroncalCommand(t, "todo", "get", uid, "--recurrence-id", "2026-10-31T00:00:00Z", "-o", "json")
	if err != nil || !strings.Contains(stdout, `"status": "COMPLETED"`) || !strings.Contains(stdout, `"due_date": "2026-10-31"`) {
		t.Fatalf("completed occurrence = %s, %v", stdout, err)
	}

	stdout, _, err = runChroncalCommand(t, "todo", "update", uid, "--recurrence-id", "2026-11-07T00:00:00Z",
		"--summary", "Moved review", "--due", "2026-11-08", "-o", "json")
	if err != nil || !strings.Contains(stdout, `"summary": "Moved review"`) || !strings.Contains(stdout, `"recurrence_id": "2026-11-07T00:00:00Z"`) {
		t.Fatalf("todo update occurrence = %s, %v", stdout, err)
	}
	// The second edit finds the override row.
	if _, _, err := runChroncalCommand(t, "todo", "update", uid, "--recurrence-id", "2026-11-07T00:00:00Z", "--priority", "2"); err != nil {
		t.Fatalf("second todo update: %v", err)
	}

	if _, _, err := runChroncalCommand(t, "todo", "delete", uid, "--recurrence-id", "2026-11-14T00:00:00Z", "--yes"); err != nil {
		t.Fatalf("todo delete occurrence: %v", err)
	}
	_, _, err = runChroncalCommand(t, "todo", "delete", uid, "--recurrence-id", "2026-11-14T00:00:00Z", "--yes")
	if err == nil || !strings.Contains(err.Error(), "is in the trash as todo") {
		t.Fatalf("second delete error = %v, want the trash note", err)
	}

	stdout, _, err = runChroncalCommand(t, "todo", "list", "--from", "2026-10-01", "--to", "2026-12-31", "-o", "json")
	if err != nil {
		t.Fatalf("todo list: %v", err)
	}
	var listed []struct {
		Summary string `json:"summary"`
		Status  string `json:"status"`
		DueDate string `json:"due_date"`
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatalf("todo list output %q: %v", stdout, err)
	}
	got := map[string]string{}
	for _, td := range listed {
		got[td.DueDate] = td.Summary + "/" + td.Status
	}
	want := map[string]string{
		"2026-10-24": "Weekly review/NEEDS-ACTION",
		"2026-10-31": "Weekly review/COMPLETED",
		"2026-11-08": "Moved review/NEEDS-ACTION",
	}
	if len(got) != len(want) {
		t.Fatalf("occurrences = %v, want %v", got, want)
	}
	for due, w := range want {
		if got[due] != w {
			t.Errorf("occurrence %s = %q, want %q", due, got[due], w)
		}
	}
}

// TestTodoOccurrenceErrorsNameTheCause checks the errors of a recurrence
// ID that names no occurrence. The old error said "not found" for a UID
// that exists.
func TestTodoOccurrenceErrorsNameTheCause(t *testing.T) {
	setupCalendarCLITestEnv(t)
	uid := addWeeklyTodo(t)
	stdout, _, err := runChroncalCommand(t, "todo", "add", "Once", "--due", "2026-10-24", "-o", "json")
	if err != nil {
		t.Fatalf("todo add: %v", err)
	}
	var single struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(stdout), &single); err != nil {
		t.Fatalf("todo add output %q: %v", stdout, err)
	}

	tests := []struct {
		args []string
		want string
	}{
		{[]string{"todo", "complete", uid, "--recurrence-id", "2026-10-25T00:00:00Z"}, "is not an occurrence of todo " + uid},
		{[]string{"todo", "update", uid, "--recurrence-id", "2026-12-26T00:00:00Z", "--summary", "x"}, "is not an occurrence of todo " + uid},
		{[]string{"todo", "delete", single.UID, "--recurrence-id", "2026-10-24T00:00:00Z", "--yes"}, "does not recur"},
		{[]string{"todo", "complete", "no-such-uid", "--recurrence-id", "2026-10-24T00:00:00Z"}, "todo no-such-uid not found"},
		{[]string{"todo", "update", uid, "--recurrence-id", "2026-10-31T00:00:00Z", "--recurrence-rule", "FREQ=DAILY"}, "--recurrence-rule changes the series"},
		{[]string{"todo", "update", uid, "--recurrence-id", "tomorrow"}, "invalid RFC 3339 timestamp"},
	}
	for _, tc := range tests {
		_, _, err := runChroncalCommand(t, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error = %v, want %q", tc.args, err, tc.want)
		}
	}
}
