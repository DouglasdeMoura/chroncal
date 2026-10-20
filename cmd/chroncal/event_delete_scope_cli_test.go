package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func addDailySeries(t *testing.T, title, date string) {
	t.Helper()
	if _, _, err := runChroncalCommand(t,
		"event", "add", title,
		"--calendar", "Work",
		"--date", date,
		"--time", "09:00",
		"--duration", "30m",
		"--recurrence-rule", "FREQ=DAILY;COUNT=4",
	); err != nil {
		t.Fatalf("event add %s: %v", title, err)
	}
}

func listSeriesStarts(t *testing.T, from, to string) []string {
	t.Helper()
	return listSeriesStartsIn(t, "Work", from, to)
}

func listSeriesStartsIn(t *testing.T, calendar, from, to string) []string {
	t.Helper()
	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--calendar", calendar,
		"--from", from,
		"--to", to,
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list: %v (stdout=%q)", err, stdout)
	}
	var events []map[string]any
	if err := json.Unmarshal([]byte(stdout), &events); err != nil {
		t.Fatalf("decode list: %v stdout=%q", err, stdout)
	}
	starts := make([]string, 0, len(events))
	for _, e := range events {
		starts = append(starts, stringFromJSON(e["start_time"]))
	}
	return starts
}

func stringFromJSON(v any) string {
	s, _ := v.(string)
	return s
}

func TestEventDelete_RecurrenceIDExcludesGeneratedOccurrence(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	addDailySeries(t, "Standup", "2026-04-06")

	got, _, err := runChroncalCommand(t, "event", "get", "1", "--output", "json")
	if err != nil {
		t.Fatalf("event get: %v", err)
	}
	var master map[string]any
	if err := json.Unmarshal([]byte(got), &master); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	uid := stringFromJSON(master["uid"])

	stdout, _, err := runChroncalCommand(t,
		"event", "delete", uid,
		"--recurrence-id", "2026-04-07T09:00:00Z",
		"--yes",
	)
	if err != nil {
		t.Fatalf("delete occurrence: %v (stdout=%q)", err, stdout)
	}
	if !strings.Contains(stdout, "Deleted occurrence") {
		t.Fatalf("expected occurrence success, got %q", stdout)
	}

	starts := listSeriesStarts(t, "2026-04-06", "2026-04-10")
	want := []string{
		"2026-04-06T09:00:00Z",
		"2026-04-08T09:00:00Z",
		"2026-04-09T09:00:00Z",
	}
	if strings.Join(starts, ",") != strings.Join(want, ",") {
		t.Fatalf("starts after occurrence delete = %v, want %v", starts, want)
	}
}

func TestEventDelete_FollowingTruncatesSeries(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	addDailySeries(t, "Standup", "2026-04-06")

	stdout, _, err := runChroncalCommand(t,
		"event", "delete", "1",
		"--following", "2026-04-08T09:00:00Z",
		"--yes",
	)
	if err != nil {
		t.Fatalf("delete following: %v (stdout=%q)", err, stdout)
	}
	if !strings.Contains(stdout, "and following") {
		t.Fatalf("expected following success, got %q", stdout)
	}

	starts := listSeriesStarts(t, "2026-04-06", "2026-04-10")
	want := []string{
		"2026-04-06T09:00:00Z",
		"2026-04-07T09:00:00Z",
	}
	if strings.Join(starts, ",") != strings.Join(want, ",") {
		t.Fatalf("starts after following delete = %v, want %v", starts, want)
	}
}

func TestEventDelete_FollowingAndSeriesMutuallyExclusive(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	addDailySeries(t, "Standup", "2026-04-06")

	_, stderr, err := runChroncalCommand(t,
		"event", "delete", "1",
		"--following", "2026-04-08T09:00:00Z",
		"--series",
		"--yes",
	)
	if err == nil {
		t.Fatal("expected exclusive-flag error")
	}
	if !strings.Contains(stderr, "--series") {
		t.Fatalf("expected exclusivity error, stderr=%q", stderr)
	}
}

// TestEventDelete_ScopeNeedsCalendarWhenUIDIsInTwoCalendars covers issue
// #842. Two calendars hold a series with the same UID. A bare UID names no
// single series, so the scoped delete refuses it and names --calendar.
// With --calendar, the delete touches that series only.
func TestEventDelete_ScopeNeedsCalendarWhenUIDIsInTwoCalendars(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	for _, name := range []string{"Work", "Home"} {
		if _, _, err := runChroncalCommand(t, "calendar", "create", name); err != nil {
			t.Fatalf("calendar create %s: %v", name, err)
		}
	}
	addDailySeries(t, "Standup", "2026-04-06")

	got, _, err := runChroncalCommand(t, "event", "get", "1", "--output", "json")
	if err != nil {
		t.Fatalf("event get: %v", err)
	}
	var master map[string]any
	if err := json.Unmarshal([]byte(got), &master); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	uid := stringFromJSON(master["uid"])

	ics := filepath.Join(t.TempDir(), "home.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nDTSTART:20260406T090000Z\r\nDTEND:20260406T093000Z\r\n" +
		"RRULE:FREQ=DAILY;COUNT=4\r\nSUMMARY:Standup Home\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Home"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}

	_, stderr, err := runChroncalCommand(t,
		"event", "delete", uid,
		"--following", "2026-04-08T09:00:00Z",
		"--yes",
	)
	if err == nil {
		t.Fatal("expected a refusal of the bare UID in two calendars")
	}
	if !strings.Contains(stderr, "more than one calendar") || !strings.Contains(stderr, "--calendar") {
		t.Fatalf("refusal must name the calendar flag, got %q", stderr)
	}

	stdout, stderr, err := runChroncalCommand(t,
		"event", "delete", uid,
		"--calendar", "Home",
		"--following", "2026-04-08T09:00:00Z",
		"--yes",
	)
	if err != nil {
		t.Fatalf("delete following in Home: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}

	if starts := listSeriesStartsIn(t, "Work", "2026-04-06", "2026-04-10"); len(starts) != 4 {
		t.Fatalf("Work starts after the Home delete = %v, want 4 untouched occurrences", starts)
	}
	want := []string{"2026-04-06T09:00:00Z", "2026-04-07T09:00:00Z"}
	if starts := listSeriesStartsIn(t, "Home", "2026-04-06", "2026-04-10"); strings.Join(starts, ",") != strings.Join(want, ",") {
		t.Fatalf("Home starts after the following delete = %v, want %v", starts, want)
	}
}
