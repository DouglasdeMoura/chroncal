package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	uid := seedSameUIDInWorkAndHome(t)

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

// seedSameUIDInWorkAndHome stores a daily series in Work and a copy with
// the same UID in Home. It returns the UID.
func seedSameUIDInWorkAndHome(t *testing.T) string {
	t.Helper()
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
	return uid
}

// TestEventDelete_SeriesHonoursCalendar: --series with --calendar deletes
// the series of that calendar only.
func TestEventDelete_SeriesHonoursCalendar(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)

	_, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--series", "--yes")
	if err == nil || !strings.Contains(stderr, "more than one calendar") {
		t.Fatalf("bare UID with --series: err = %v, stderr = %q; want the ambiguity hint", err, stderr)
	}

	stdout, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--series", "--calendar", "Home", "--yes")
	if err != nil {
		t.Fatalf("delete series in Home: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Deleted event series") {
		t.Fatalf("expected the series success, got %q", stdout)
	}
	if starts := listSeriesStartsIn(t, "Work", "2026-04-06", "2026-04-10"); len(starts) != 4 {
		t.Fatalf("Work starts after the Home delete = %v, want 4 untouched occurrences", starts)
	}
	if starts := listSeriesStartsIn(t, "Home", "2026-04-06", "2026-04-10"); len(starts) != 0 {
		t.Fatalf("Home starts after the series delete = %v, want none", starts)
	}
}

// TestEventDelete_NumericIDMustMatchCalendar: a row ID with a --calendar
// that does not hold the row is a contradiction.
func TestEventDelete_NumericIDMustMatchCalendar(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	seedSameUIDInWorkAndHome(t)

	for _, args := range [][]string{
		{"event", "delete", "1", "--calendar", "Home", "--following", "2026-04-08T09:00:00Z", "--yes"},
		{"event", "delete", "1", "--calendar", "Home", "--series", "--yes"},
	} {
		_, stderr, err := runChroncalCommand(t, args...)
		if err == nil || !strings.Contains(stderr, "not in --calendar") {
			t.Fatalf("%v: err = %v, stderr = %q; want the calendar mismatch error", args, err, stderr)
		}
	}
	if starts := listSeriesStartsIn(t, "Work", "2026-04-06", "2026-04-10"); len(starts) != 4 {
		t.Fatalf("Work starts after the refused deletes = %v, want 4", starts)
	}
	stdout, stderr, err := runChroncalCommand(t, "event", "delete", "1", "--calendar", "Work", "--following", "2026-04-08T09:00:00Z", "--yes")
	if err != nil {
		t.Fatalf("delete following in Work by ID: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if starts := listSeriesStartsIn(t, "Work", "2026-04-06", "2026-04-10"); len(starts) != 2 {
		t.Fatalf("Work starts after the truncation = %v, want 2", starts)
	}
}

// TestEventRestore_SeriesInTwoCalendarsByMasterID: with the UID in the
// trash of two calendars, the restore by UID names no series, and the ID
// of a master restores that series.
func TestEventRestore_SeriesInTwoCalendarsByMasterID(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)

	stdout, _, err := runChroncalCommand(t, "event", "list", "--calendar", "Home", "--from", "2026-04-06", "--to", "2026-04-07", "--output", "json")
	if err != nil {
		t.Fatalf("event list Home: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) == 0 {
		t.Fatalf("decode list: %v stdout=%q", err, stdout)
	}
	homeID := int64(rows[0]["id"].(float64))

	for _, args := range [][]string{
		{"event", "delete", "1", "--series", "--yes"},
		{"event", "delete", uid, "--series", "--calendar", "Home", "--yes"},
	} {
		if stdout, stderr, err := runChroncalCommand(t, args...); err != nil {
			t.Fatalf("%v: %v (stdout=%q stderr=%q)", args, err, stdout, stderr)
		}
	}
	_, stderr, err := runChroncalCommand(t, "event", "restore", uid)
	if err == nil || !strings.Contains(stderr, "more than one calendar") || !strings.Contains(stderr, "master") {
		t.Fatalf("restore by UID: err = %v, stderr = %q; want the master-ID hint", err, stderr)
	}
	stdout, stderr, err = runChroncalCommand(t, "event", "restore", strconv.FormatInt(homeID, 10))
	if err != nil {
		t.Fatalf("restore Home master: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if starts := listSeriesStartsIn(t, "Home", "2026-04-06", "2026-04-10"); len(starts) != 4 {
		t.Fatalf("Home starts after the restore = %v, want 4", starts)
	}
	if starts := listSeriesStartsIn(t, "Work", "2026-04-06", "2026-04-10"); len(starts) != 0 {
		t.Fatalf("Work starts after the restore of Home = %v, want none", starts)
	}
}

// TestEventUpdate_RecurrenceIDNamesSeriesCalendar: with the UID in two
// calendars, an occurrence edit by UID needs --calendar to name the
// series, and the edit then touches that series only.
func TestEventUpdate_RecurrenceIDNamesSeriesCalendar(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	day2 := "2026-04-07T09:00:00Z"

	_, stderr, err := runChroncalCommand(t, "event", "update", uid, "--recurrence-id", day2, "--title", "Moved")
	if err == nil || !strings.Contains(stderr, "more than one calendar") || !strings.Contains(stderr, "--calendar") {
		t.Fatalf("update by UID: err = %v, stderr = %q; want the --calendar hint", err, stderr)
	}
	stdout, stderr, err := runChroncalCommand(t, "event", "update", uid, "--recurrence-id", day2, "--calendar", "Home", "--title", "Moved")
	if err != nil {
		t.Fatalf("update in Home: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	for _, c := range []struct{ calendar, want string }{{"Home", "Moved"}, {"Work", "Standup"}} {
		out, _, err := runChroncalCommand(t, "event", "list", "--calendar", c.calendar, "--from", "2026-04-07", "--to", "2026-04-08", "--output", "json")
		if err != nil {
			t.Fatalf("event list %s: %v", c.calendar, err)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("decode list %s: %v stdout=%q", c.calendar, err, out)
		}
		var got string
		for _, r := range rows {
			if stringFromJSON(r["start_time"]) == day2 {
				got = stringFromJSON(r["title"])
			}
		}
		if got != c.want {
			t.Errorf("%s occurrence title = %q, want %q", c.calendar, got, c.want)
		}
	}
}

// TestEventDelete_ReusedCommandTreeKeepsNoCalendar runs one command tree
// twice in one process. The first run names --calendar; the second run
// clears it, and the delete must not keep the first calendar.
func TestEventDelete_ReusedCommandTreeKeepsNoCalendar(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)

	// The root command loads the config (CHRONCAL_DB) in its persistent
	// pre-run, so the in-process runs go through it. The tree is the one
	// global tree, so the second run sees the flags of the first.
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	rootCmd.SetArgs([]string{"event", "delete", uid, "--series", "--calendar", "Home", "--yes"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("first run (Home): %v", err)
	}
	// The Work master has row ID 1. A stale Home calendar would refuse it.
	rootCmd.SetArgs([]string{"event", "delete", "1", "--series", "--calendar", "", "--yes"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("second run (no calendar): %v", err)
	}
	for _, cal := range []string{"Work", "Home"} {
		if starts := listSeriesStartsIn(t, cal, "2026-04-06", "2026-04-10"); len(starts) != 0 {
			t.Errorf("%s starts after both deletes = %v, want none", cal, starts)
		}
	}
}

// TestEventUpdate_CalendarWithoutSeriesNamesIt: with --calendar on a
// calendar that holds no series of the UID, the error names that calendar
// instead of the generic "pass --calendar" hint.
func TestEventUpdate_CalendarWithoutSeriesNamesIt(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Empty"); err != nil {
		t.Fatalf("calendar create Empty: %v", err)
	}
	_, stderr, err := runChroncalCommand(t, "event", "update", uid, "--recurrence-id", "2026-04-07T09:00:00Z", "--calendar", "Empty", "--title", "Moved")
	if err == nil {
		t.Fatal("update with a calendar that holds no series succeeded")
	}
	if !strings.Contains(stderr, `calendar "Empty" holds no series`) || !strings.Contains(stderr, "name the calendar of the series") {
		t.Fatalf("stderr = %q, want the named calendar and the remedy", stderr)
	}
}

// seedLegacyOverrideInHome stores a daily series in Work and an override
// of its second occurrence in Home with no master there (a legacy
// override). It returns the UID and the row ID of the legacy override.
func seedLegacyOverrideInHome(t *testing.T) (string, int64) {
	t.Helper()
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
	ics := filepath.Join(t.TempDir(), "legacy.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T100000Z\r\nDTEND:20260407T103000Z\r\n" +
		"SUMMARY:Legacy late\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Home"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	// An orphan override is not in the expanded list, so the row comes
	// from a get by UID and recurrence ID.
	out, _, err := runChroncalCommand(t, "event", "get", uid, "--recurrence-id", "2026-04-07T09:00:00Z", "--output", "json")
	if err != nil {
		t.Fatalf("event get legacy override: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(out), &row); err != nil {
		t.Fatalf("decode get: %v stdout=%q", err, out)
	}
	return uid, int64(row["id"].(float64))
}

// TestEventUpdate_CalendarOfLegacyOverrideNamesSeries: --calendar on the
// calendar of a legacy override names the series of its master, so an
// edit of another occurrence creates the override beside the master and
// is not a move.
func TestEventUpdate_CalendarOfLegacyOverrideNamesSeries(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid, _ := seedLegacyOverrideInHome(t)
	day3 := "2026-04-08T09:00:00Z"
	stdout, stderr, err := runChroncalCommand(t, "event", "update", uid, "--recurrence-id", day3, "--calendar", "Home", "--title", "Moved")
	if err != nil {
		t.Fatalf("update through the legacy calendar: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	out, _, err := runChroncalCommand(t, "event", "list", "--calendar", "Work", "--from", "2026-04-08", "--to", "2026-04-09", "--output", "json")
	if err != nil {
		t.Fatalf("event list Work: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decode list Work: %v stdout=%q", err, out)
	}
	var got string
	for _, r := range rows {
		if stringFromJSON(r["start_time"]) == day3 {
			got = stringFromJSON(r["title"])
		}
	}
	if got != "Moved" {
		t.Errorf("Work occurrence of %s = %q, want the edit beside the master", day3, got)
	}
}

// TestEventDelete_LegacyOverrideRowActsOnMaster: a delete of an occurrence
// through the row ID of a legacy override acts on the master of its
// series, in the calendar of the master.
func TestEventDelete_LegacyOverrideRowActsOnMaster(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	_, legacyID := seedLegacyOverrideInHome(t)
	stdout, stderr, err := runChroncalCommand(t, "event", "delete", strconv.FormatInt(legacyID, 10), "--recurrence-id", "2026-04-07T09:00:00Z", "--yes")
	if err != nil {
		t.Fatalf("delete through the legacy row: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	want := []string{"2026-04-06T09:00:00Z", "2026-04-08T09:00:00Z", "2026-04-09T09:00:00Z"}
	if starts := listSeriesStartsIn(t, "Work", "2026-04-06", "2026-04-10"); strings.Join(starts, ",") != strings.Join(want, ",") {
		t.Fatalf("Work starts after the delete = %v, want %v", starts, want)
	}
}

// TestEventGet_UIDTrashedInTwoCalendarsIsNotFound: a UID whose series are
// in the trash of two calendars is not found by a live lookup.
func TestEventGet_UIDTrashedInTwoCalendarsIsNotFound(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	for _, args := range [][]string{
		{"event", "delete", "1", "--series", "--yes"},
		{"event", "delete", uid, "--series", "--calendar", "Home", "--yes"},
	} {
		if stdout, stderr, err := runChroncalCommand(t, args...); err != nil {
			t.Fatalf("%v: %v (stdout=%q stderr=%q)", args, err, stdout, stderr)
		}
	}
	for _, args := range [][]string{
		{"event", "get", uid},
		{"event", "update", uid, "--title", "x"},
		{"event", "rsvp", uid, "--status", "ACCEPTED"},
	} {
		_, stderr, err := runChroncalCommand(t, args...)
		if err == nil || !strings.Contains(stderr, "not found") || strings.Contains(stderr, "more than one calendar") {
			t.Errorf("%v: err = %v, stderr = %q; want not found", args, err, stderr)
		}
	}
}

// TestEventDelete_TwoLegacyOverridesNameTheRows: two orphan imports of one
// occurrence into two calendars give an error that names the rows.
func TestEventDelete_TwoLegacyOverridesNameTheRows(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid, legacyID := seedLegacyOverrideInHome(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Other"); err != nil {
		t.Fatalf("calendar create Other: %v", err)
	}
	ics := filepath.Join(t.TempDir(), "legacy2.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T110000Z\r\nDTEND:20260407T113000Z\r\n" +
		"SUMMARY:Legacy other\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Other"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	_, stderr, err := runChroncalCommand(t, "event", "delete", "1", "--recurrence-id", "2026-04-07T09:00:00Z", "--yes")
	if err == nil {
		t.Fatal("delete with two override rows of one occurrence succeeded")
	}
	if !strings.Contains(stderr, "more than one override row") || !strings.Contains(stderr, strconv.FormatInt(legacyID, 10)) || strings.Contains(stderr, "--calendar") {
		t.Fatalf("stderr = %q, want the override rows named and no --calendar hint", stderr)
	}
}

// TestEventUpdate_CalendarOfMasterEditsLegacyOverride: --calendar on the
// calendar of the master still edits the stored legacy override of the
// occurrence, instead of a generated occurrence over it.
func TestEventUpdate_CalendarOfMasterEditsLegacyOverride(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid, legacyID := seedLegacyOverrideInHome(t)
	day2 := "2026-04-07T09:00:00Z"
	stdout, stderr, err := runChroncalCommand(t, "event", "update", uid, "--recurrence-id", day2, "--calendar", "Work", "--description", "Notes")
	if err != nil {
		t.Fatalf("update through the master calendar: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	out, _, err := runChroncalCommand(t, "event", "get", strconv.FormatInt(legacyID, 10), "--output", "json")
	if err != nil {
		t.Fatalf("event get legacy: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(out), &row); err != nil {
		t.Fatalf("decode get: %v stdout=%q", err, out)
	}
	if got := stringFromJSON(row["title"]); got != "Legacy late" {
		t.Errorf("legacy title after the edit = %q, want it kept", got)
	}
	if got := stringFromJSON(row["description"]); got != "Notes" {
		t.Errorf("legacy description after the edit = %q, want Notes", got)
	}
}

// TestEventUpdate_LegacyCalendarWithTwoMastersNamesIt: --calendar on a
// calendar that holds a legacy override of a UID with two live masters
// names that calendar in the error, not the generic --calendar hint.
func TestEventUpdate_LegacyCalendarWithTwoMastersNamesIt(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Other"); err != nil {
		t.Fatalf("calendar create Other: %v", err)
	}
	ics := filepath.Join(t.TempDir(), "legacy.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T100000Z\r\nDTEND:20260407T103000Z\r\n" +
		"SUMMARY:Legacy\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Other"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	_, stderr, err := runChroncalCommand(t, "event", "update", uid, "--recurrence-id", "2026-04-07T09:00:00Z", "--calendar", "Other", "--title", "x")
	if err == nil {
		t.Fatal("update through a legacy calendar with two live masters succeeded")
	}
	if !strings.Contains(stderr, `in calendar "Other" belong to no series`) || !strings.Contains(stderr, "use the ID of the row") || strings.Contains(stderr, "pass --calendar") {
		t.Fatalf("stderr = %q, want the cause in the named calendar and the row-ID remedy", stderr)
	}
}

// TestEventGet_TwoLegacyOverridesNameTheRows: a get of one occurrence by
// UID with two legacy rows names the rows.
func TestEventGet_TwoLegacyOverridesNameTheRows(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid, legacyID := seedLegacyOverrideInHome(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Other"); err != nil {
		t.Fatalf("calendar create Other: %v", err)
	}
	ics := filepath.Join(t.TempDir(), "legacy2.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T110000Z\r\nDTEND:20260407T113000Z\r\n" +
		"SUMMARY:Legacy other\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Other"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	_, stderr, err := runChroncalCommand(t, "event", "get", uid, "--recurrence-id", "2026-04-07T09:00:00Z")
	if err == nil {
		t.Fatal("get with two override rows of one occurrence succeeded")
	}
	if !strings.Contains(stderr, "more than one override row") || !strings.Contains(stderr, strconv.FormatInt(legacyID, 10)) {
		t.Fatalf("stderr = %q, want the override rows named", stderr)
	}
}

// TestEventDelete_CalendarWithTwoMastersElsewhereNamesIt: --calendar on a
// legacy calendar of a UID with two live masters names that calendar in
// the error of a scoped delete, not the generic --calendar hint.
func TestEventDelete_CalendarWithTwoMastersElsewhereNamesIt(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Other"); err != nil {
		t.Fatalf("calendar create Other: %v", err)
	}
	ics := filepath.Join(t.TempDir(), "legacy.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T100000Z\r\nDTEND:20260407T103000Z\r\n" +
		"SUMMARY:Legacy\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Other"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	_, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--calendar", "Other", "--following", "2026-04-08T09:00:00Z", "--yes")
	if err == nil {
		t.Fatal("delete through a legacy calendar with two live masters succeeded")
	}
	if !strings.Contains(stderr, `in calendar "Other" belong to no series`) || !strings.Contains(stderr, "use the ID of the row") || strings.Contains(stderr, "pass --calendar") {
		t.Fatalf("stderr = %q, want the cause in the named calendar and the row-ID remedy", stderr)
	}
}

// TestEventDelete_PlainWithCalendarTargetsTheRowThere: a plain delete with
// --calendar on the calendar of a legacy override deletes that row, not
// the master of its series in another calendar. A row ID of the legacy
// override with the calendar of its master is accepted too.
func TestEventDelete_PlainWithCalendarTargetsTheRowThere(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid, legacyID := seedLegacyOverrideInHome(t)
	stdout, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--calendar", "Home", "--yes")
	if err != nil {
		t.Fatalf("plain delete in Home: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Deleted event "+strconv.FormatInt(legacyID, 10)) {
		t.Fatalf("stdout = %q, want the legacy row %d deleted", stdout, legacyID)
	}
	if _, _, err := runChroncalCommand(t, "event", "get", "1"); err != nil {
		t.Fatalf("the Work master after the delete in Home: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "event", "restore", strconv.FormatInt(legacyID, 10)); err != nil {
		t.Fatalf("restore the legacy row: %v", err)
	}
	stdout, stderr, err = runChroncalCommand(t, "event", "delete", strconv.FormatInt(legacyID, 10), "--calendar", "Work", "--yes")
	if err != nil {
		t.Fatalf("delete the legacy row by ID with --calendar Work: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
}

// TestEventDelete_PlainWithCalendarRefusesSeveralRows: a plain delete with
// --calendar on a calendar that holds several rows of the UID and no
// master names the rows to choose from.
func TestEventDelete_PlainWithCalendarRefusesSeveralRows(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid, legacyID := seedLegacyOverrideInHome(t)
	ics := filepath.Join(t.TempDir(), "legacy2.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260408T090000Z\r\nDTSTART:20260408T100000Z\r\nDTEND:20260408T103000Z\r\n" +
		"SUMMARY:Legacy two\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Home"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	_, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--calendar", "Home", "--yes")
	if err == nil {
		t.Fatal("plain delete of several rows in Home succeeded")
	}
	if !strings.Contains(stderr, "rows of event") || !strings.Contains(stderr, strconv.FormatInt(legacyID, 10)) {
		t.Fatalf("stderr = %q, want the row IDs", stderr)
	}
}

// TestEventDelete_SeriesWithCalendarReachesTheService: a series delete of
// a UID with --calendar goes to the service. A calendar with orphan rows
// and no live master anywhere has its rows deleted. A calendar whose
// series is in the trash, beside a live series elsewhere, names the trash.
func TestEventDelete_SeriesWithCalendarReachesTheService(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	if stdout, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--series", "--calendar", "Home", "--yes"); err != nil {
		t.Fatalf("first series delete in Home: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	_, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--series", "--calendar", "Home", "--yes")
	if err == nil || !strings.Contains(stderr, "is in the trash in calendar") {
		t.Fatalf("second series delete in Home: err = %v, stderr = %q; want the trash message", err, stderr)
	}

	const orphan = "orphan-only-uid"
	ics := filepath.Join(t.TempDir(), "orphan.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + orphan +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T100000Z\r\nDTEND:20260407T103000Z\r\n" +
		"SUMMARY:Orphan\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Work"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	stdout, stderr, err := runChroncalCommand(t, "event", "delete", orphan, "--series", "--calendar", "Work", "--yes")
	if err != nil {
		t.Fatalf("series delete of orphan rows: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Deleted event series") {
		t.Fatalf("stdout = %q, want the series delete", stdout)
	}
	if _, _, err := runChroncalCommand(t, "event", "get", orphan, "--recurrence-id", "2026-04-07T09:00:00Z"); err == nil {
		t.Fatal("the orphan row is live after its series delete")
	}
}
