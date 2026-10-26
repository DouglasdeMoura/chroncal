package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// addWeeklyJournal adds a weekly journal entry with three occurrences, on
// 2026-10-19, 2026-10-26, and 2026-11-02. It returns the UID.
func addWeeklyJournal(t *testing.T) string {
	t.Helper()
	stdout, _, err := runChroncalCommand(t, "journal", "add", "Weekly log",
		"--date", "2026-10-19", "--rrule", "FREQ=WEEKLY;COUNT=3", "-o", "json")
	if err != nil {
		t.Fatalf("journal add: %v", err)
	}
	var out struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.UID == "" {
		t.Fatalf("journal add output %q: %v", stdout, err)
	}
	return out.UID
}

// TestJournalOccurrenceCommands locks in issue #916. With --recurrence-id,
// "journal update" and "journal delete" act on one occurrence of a
// recurring journal entry that has no override row yet.
func TestJournalOccurrenceCommands(t *testing.T) {
	setupCalendarCLITestEnv(t)
	uid := addWeeklyJournal(t)

	stdout, _, err := runChroncalCommand(t, "journal", "update", uid, "--recurrence-id", "2026-10-26", "--summary", "Short week", "-o", "json")
	if err != nil || !strings.Contains(stdout, `"summary": "Short week"`) || !strings.Contains(stdout, `"recurrence_id": "2026-10-26T00:00:00Z"`) {
		t.Fatalf("journal update occurrence = %s, %v", stdout, err)
	}
	// The second edit finds the override row, also with a stray space.
	if _, _, err := runChroncalCommand(t, "journal", "update", uid, "--recurrence-id", " 2026-10-26T00:00:00Z", "--status", "DRAFT"); err != nil {
		t.Fatalf("second journal update: %v", err)
	}

	if _, _, err := runChroncalCommand(t, "journal", "delete", uid, "--recurrence-id", "2026-11-02", "--yes"); err != nil {
		t.Fatalf("journal delete occurrence: %v", err)
	}
	_, _, err = runChroncalCommand(t, "journal", "delete", uid, "--recurrence-id", "2026-11-02", "--yes")
	if err == nil || !strings.Contains(err.Error(), "is in the trash as journal") {
		t.Fatalf("second delete error = %v, want the trash note", err)
	}

	stdout, _, err = runChroncalCommand(t, "journal", "list", "--from", "2026-10-01", "--to", "2026-12-31", "-o", "json")
	if err != nil {
		t.Fatalf("journal list: %v", err)
	}
	var listed []struct {
		Summary   string `json:"summary"`
		Status    string `json:"status"`
		StartDate string `json:"start_date"`
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatalf("journal list output %q: %v", stdout, err)
	}
	got := map[string]string{}
	for _, j := range listed {
		got[j.StartDate] = j.Summary + "/" + j.Status
	}
	want := map[string]string{
		"2026-10-19": "Weekly log/FINAL",
		"2026-10-26": "Short week/DRAFT",
	}
	if len(got) != len(want) {
		t.Fatalf("occurrences = %v, want %v", got, want)
	}
	for date, w := range want {
		if got[date] != w {
			t.Errorf("occurrence %s = %q, want %q", date, got[date], w)
		}
	}
}

// TestJournalOccurrenceErrorsNameTheCause checks the errors of a recurrence
// ID that names no occurrence. The old error said "not found" for a UID
// that exists.
func TestJournalOccurrenceErrorsNameTheCause(t *testing.T) {
	setupCalendarCLITestEnv(t)
	uid := addWeeklyJournal(t)
	addUID := func(args ...string) string {
		t.Helper()
		stdout, _, err := runChroncalCommand(t, append([]string{"journal", "add"}, append(args, "-o", "json")...)...)
		if err != nil {
			t.Fatalf("journal add %v: %v", args, err)
		}
		var out struct {
			UID string `json:"uid"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("journal add output %q: %v", stdout, err)
		}
		return out.UID
	}
	single := addUID("Once", "--date", "2026-10-19")
	cancelled := addUID("Cancelled", "--date", "2026-10-19", "--rrule", "FREQ=WEEKLY", "--status", "CANCELLED")

	tests := []struct {
		args []string
		want string
	}{
		{[]string{"journal", "update", uid, "--recurrence-id", "2026-10-20", "--summary", "x"}, "is not an occurrence of journal " + uid},
		{[]string{"journal", "delete", uid, "--recurrence-id", "2026-11-09", "--yes"}, "is not an occurrence of journal " + uid},
		{[]string{"journal", "delete", single, "--recurrence-id", "2026-10-19", "--yes"}, "does not recur"},
		{[]string{"journal", "update", "no-such-uid", "--recurrence-id", "2026-10-19", "--summary", "x"}, "journal no-such-uid not found"},
		{[]string{"journal", "update", uid, "--recurrence-id", "2026-10-26", "--rrule", "FREQ=DAILY"}, "--rrule changes the series"},
		{[]string{"journal", "update", uid, "--recurrence-id", "tomorrow"}, "use RFC 3339 or YYYY-MM-DD"},
		{[]string{"journal", "update", cancelled, "--recurrence-id", "2026-10-26", "--summary", "x"}, "is cancelled, so it has no occurrences"},
	}
	for _, tc := range tests {
		_, _, err := runChroncalCommand(t, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error = %v, want %q", tc.args, err, tc.want)
		}
	}
}

// TestJournalOccurrenceInTheTrashOfATrashedSeries names the series when its
// master is in the trash too. A restore of the override alone would leave
// an override with no series.
func TestJournalOccurrenceInTheTrashOfATrashedSeries(t *testing.T) {
	setupCalendarCLITestEnv(t)
	uid := addWeeklyJournal(t)
	if _, _, err := runChroncalCommand(t, "journal", "delete", uid, "--recurrence-id", "2026-10-26", "--yes"); err != nil {
		t.Fatalf("journal delete occurrence: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "journal", "delete", uid, "--series", "--yes"); err != nil {
		t.Fatalf("journal delete series: %v", err)
	}
	_, _, err := runChroncalCommand(t, "journal", "update", uid, "--recurrence-id", "2026-10-26", "--summary", "x")
	if err == nil || !strings.Contains(err.Error(), "the series of journal "+uid+" is in the trash") {
		t.Fatalf("error = %v, want the series in the trash", err)
	}
}
