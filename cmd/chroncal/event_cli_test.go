package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestFormatCompactEventUsesSharedTableLayout(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 21, 9, 0, 0, 0, time.Local)
	e := event.Event{
		ID:         42,
		CalendarID: 7,
		Title:      "Team Standup",
		StartTime:  start,
		EndTime:    start.Add(30 * time.Minute),
		Categories: "work,meeting",
	}
	header := formatCompactEventHeader(true, false)
	if got, want := strings.Fields(header), []string{"ID", "DATE", "TIME", "CATEGORIES", "CALENDAR", "SUMMARY"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event compact header fields = %v, want %v", got, want)
	}
	got := formatCompactEvent(e, map[int64]string{7: "Work"}, true, false)
	want := "42    2026-04-21             09:00-09:30  work,meeting        Work              Team Standup"
	if got != want {
		t.Fatalf("formatCompactEvent() = %q, want %q", got, want)
	}
	colored := formatCompactEvent(e, map[int64]string{7: "Work"}, true, true)
	for _, code := range []string{"\x1b[1;36m", "\x1b[2m", "\x1b[33m", "\x1b[35m"} {
		if !strings.Contains(colored, code) {
			t.Fatalf("colored event row = %q, want ANSI code %q", colored, code)
		}
	}
}

func TestEventListCompactIDCanBePassedToGet(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Team Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
		"--categories", "work,meeting",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list", "--compact", "--show-calendar",
		"--calendar", "Work",
		"--from", "2026-04-21",
		"--to", "2026-04-22",
	)
	if err != nil {
		t.Fatalf("event list --compact: %v", err)
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Fatalf("piped compact output contains ANSI styling: %q", stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) < 2 {
		t.Fatalf("compact output contains no data row: %q", stdout)
	}
	if !strings.Contains(lines[0], "ID") || !strings.Contains(lines[0], "CALENDAR") ||
		!strings.Contains(lines[1], "meeting,work") || !strings.Contains(lines[1], "Work") {
		t.Fatalf("compact event table missing expected columns: %q", stdout)
	}
	id := strings.Fields(lines[1])[0]
	getOut, _, err := runChroncalCommand(t, "event", "get", id)
	if err != nil {
		t.Fatalf("event get %q: %v", id, err)
	}
	if !strings.Contains(getOut, "Team Standup") {
		t.Fatalf("event get output = %q, want summary", getOut)
	}
}

func TestEventListVerboseUsesTimeRailView(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	if _, _, err := runChroncalCommand(t,
		"event", "add", "Team Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
		"--location", "Zoom",
		"--description", "Sprint planning",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--verbose",
		"--show-id",
		"--show-calendar",
		"--calendar", "Work",
		"--from", "2026-04-21",
		"--to", "2026-04-22",
		"--show-weekday",
	)
	if err != nil {
		t.Fatalf("event list --verbose: %v", err)
	}

	wantPrefix := "" +
		"Apr 21 Tue\n" +
		"----------\n" +
		"09:00   | Team Standup ("
	if !strings.HasPrefix(stdout, wantPrefix) {
		t.Fatalf("event list --verbose output mismatch\nwant prefix:\n%s\ngot:\n%s", wantPrefix, stdout)
	}
	for _, needle := range []string{"        | Zoom\n", "        | Sprint planning\n", "Calendar: Work"} {
		if !strings.Contains(stdout, needle) {
			t.Fatalf("event list --verbose output = %q, want substring %q", stdout, needle)
		}
	}
	if strings.Contains(stdout, "uid: ") {
		t.Fatalf("event list --verbose output = %q, should not show uid", stdout)
	}
}

func TestEventListCompactCanShowEventIDAndCalendar(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	if _, _, err := runChroncalCommand(t,
		"event", "add", "Team Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--show-id",
		"--show-calendar",
		"--calendar", "Work",
		"--from", "2026-04-21",
		"--to", "2026-04-22",
		"--show-weekday",
	)
	if err != nil {
		t.Fatalf("event list compact flags: %v", err)
	}

	for _, needle := range []string{"Team Standup (", "[Work]"} {
		if !strings.Contains(stdout, needle) {
			t.Fatalf("event list output = %q, want substring %q", stdout, needle)
		}
	}
}

// TestEventAddAndUpdateShareDetailBlock locks in that event add and event
// update emit the same detail-block shape used by event get. The
// CLI then does not have one prose summary on create and a structured block
// on update.
func TestEventAddAndUpdateShareDetailBlock(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	addOut, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
	)
	if err != nil {
		t.Fatalf("event add: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(addOut), "Created:") {
		t.Fatalf("event add output starts with 'Created:' prose; want the same detail block as event get:\n%s", addOut)
	}
	for _, needle := range []string{"  Standup\n", "    when:", "    id:", "    uid:"} {
		if !strings.Contains(addOut, needle) {
			t.Fatalf("event add output = %q, missing %q", addOut, needle)
		}
	}

	updateOut, _, err := runChroncalCommand(t,
		"event", "update", "1",
		"--title", "Daily Standup",
	)
	if err != nil {
		t.Fatalf("event update: %v", err)
	}
	for _, needle := range []string{"  Daily Standup\n", "    when:", "    id:", "    uid:"} {
		if !strings.Contains(updateOut, needle) {
			t.Fatalf("event update output = %q, missing %q", updateOut, needle)
		}
	}
}

// TestNotFoundErrorHasNoWrapPrefix locks in that user-facing error
// messages don't leak the internal fmt.Errorf wrap chain (e.g.
// "get event: event 999 not found"). printCLIError prefers the
// *cliError.Msg over the outer wrapped message.
func TestNotFoundErrorHasNoWrapPrefix(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, _, err := runChroncalCommand(t, "event", "get", "999")
	if err == nil {
		t.Fatal("event get 999 should fail")
	}
	got := err.Error()
	if !strings.Contains(got, "event 999 not found") {
		t.Fatalf("error = %q, want it to contain %q", got, "event 999 not found")
	}
	if strings.Contains(got, "get event:") {
		t.Fatalf("error = %q, should not leak the 'get event:' wrap prefix", got)
	}
}

// TestEventJSONTimestampsAreUTC locks in the documented policy that
// --output json emits RFC 3339 in UTC (Z suffix) regardless of the
// terminal's TZ. Text mode keeps local time and is covered elsewhere.
func TestEventJSONTimestampsAreUTC(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "America/New_York")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--from", "2026-04-21",
		"--to", "2026-04-21",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list --output json: %v", err)
	}

	var events []struct {
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &events); jerr != nil {
		t.Fatalf("decode %q: %v", stdout, jerr)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	for _, ts := range []string{e.StartTime, e.EndTime, e.CreatedAt, e.UpdatedAt} {
		if !strings.HasSuffix(ts, "Z") {
			t.Fatalf("timestamp %q is not UTC (no Z suffix); JSON output must be in UTC", ts)
		}
	}
	// 09:00 America/New_York on 2026-04-21 is 13:00 UTC (EDT, UTC-4).
	if e.StartTime != "2026-04-21T13:00:00Z" {
		t.Fatalf("start_time = %q, want %q (09:00 EDT = 13:00 UTC)", e.StartTime, "2026-04-21T13:00:00Z")
	}
}

// TestEventSearchDateBoundsIncludeEndDay locks in the fix for issue #428.
// `event search --from/--to` accept the same YYYY-MM-DD bounds as event
// list. The half-open --to bound includes the entire end day. Before
// the fix the raw "2026-04-30" string was compared lexicographically
// against the RFC3339-stored start time ("2026-04-30T09:00:00Z"). The
// 'T' > '0' order then excluded every event on the final day in silence.
func TestEventSearchDateBoundsIncludeEndDay(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Quarterly Review",
		"--calendar", "Work",
		"--date", "2026-04-30",
		"--time", "09:00",
		"--duration", "30m",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "search", "Quarterly",
		"--from", "2026-04-01",
		"--to", "2026-04-30",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event search: %v", err)
	}

	var events []struct {
		Title string `json:"title"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &events); jerr != nil {
		t.Fatalf("decode %q: %v", stdout, jerr)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (event on the --to end day must be included)", len(events))
	}
	if events[0].Title != "Quarterly Review" {
		t.Fatalf("title = %q, want %q", events[0].Title, "Quarterly Review")
	}
}

// TestEventSearchInvalidDateBound locks in that unparseable --from/--to
// values are rejected with a clean error. They are not passed through
// verbatim into the lexicographic comparison (issue #428).
func TestEventSearchInvalidDateBound(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	_, _, err := runChroncalCommand(t, "event", "search", "anything", "--to", "not-a-date")
	if err == nil {
		t.Fatal("event search --to not-a-date should fail")
	}
	if !strings.Contains(err.Error(), "--to") {
		t.Fatalf("error = %q, want it to mention the --to flag", err.Error())
	}
}

func TestEventUpdateAllDayToTimedDefaultsToOneHour(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	// All-day event (no --time) spans 24h internally.
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Holiday",
		"--calendar", "Work",
		"--date", "2026-04-21",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	// Convert to a timed event with only --time, no --duration/--end-time.
	if _, _, err := runChroncalCommand(t,
		"event", "update", "1",
		"--time", "09:00",
	); err != nil {
		t.Fatalf("event update: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--from", "2026-04-21",
		"--to", "2026-04-21",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list --output json: %v", err)
	}

	var events []struct {
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		AllDay    bool   `json:"all_day"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &events); jerr != nil {
		t.Fatalf("decode %q: %v", stdout, jerr)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.AllDay {
		t.Fatalf("event still all-day after --time conversion; all_day = true")
	}
	if e.StartTime != "2026-04-21T09:00:00Z" {
		t.Fatalf("start_time = %q, want %q", e.StartTime, "2026-04-21T09:00:00Z")
	}
	// Converting an all-day event to timed with no explicit span should
	// default to a 1h duration (like `event add`), not preserve the 24h
	// all-day span.
	if e.EndTime != "2026-04-21T10:00:00Z" {
		t.Fatalf("end_time = %q, want %q (1h default, not 24h all-day span)", e.EndTime, "2026-04-21T10:00:00Z")
	}
}

func TestNotFoundErrorJSONHasNoWrapPrefix(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t, "event", "get", "999", "--output", "json")
	if err == nil {
		t.Fatal("event get 999 --output json should fail")
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode error payload %q: %v", stderr, jerr)
	}
	if payload.Code != "not_found" {
		t.Fatalf("code = %q, want %q", payload.Code, "not_found")
	}
	if payload.Error != "event 999 not found" {
		t.Fatalf("error = %q, want %q (no wrap prefix)", payload.Error, "event 999 not found")
	}
}

// TestEventDurationMustBePositive guards the --duration end-after-start
// contract. A zero or negative duration stored end <= start, an event no
// view or exporter can represent. The end-time path already rejected it;
// the duration path must reject it too, in add and update.
func TestEventDurationMustBePositive(t *testing.T) {
	setupCalendarCLITestEnv(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	for _, dur := range []string{"0s", "-30m"} {
		_, stderr, err := runChroncalCommand(t, "event", "add", "Standup",
			"--calendar", "Work", "--date", "2026-04-06", "--time", "09:00",
			"--duration", dur, "--output", "json")
		if err == nil {
			t.Fatalf("event add --duration %s was accepted", dur)
		}
		var payload struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
			t.Fatalf("decode error payload %q: %v", stderr, jerr)
		}
		if payload.Code != "invalid_input" {
			t.Fatalf("--duration %s: code = %q, want invalid_input", dur, payload.Code)
		}
	}

	// A positive duration still works, and update rejects the same values.
	if _, _, err := runChroncalCommand(t, "event", "add", "Standup",
		"--calendar", "Work", "--date", "2026-04-06", "--time", "09:00",
		"--duration", "30m"); err != nil {
		t.Fatalf("event add --duration 30m: %v", err)
	}
	_, stderr, err := runChroncalCommand(t, "event", "update", "1", "--duration", "0s", "--output", "json")
	if err == nil {
		t.Fatal("event update --duration 0s was accepted")
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode error payload %q: %v", stderr, jerr)
	}
	if payload.Code != "invalid_input" {
		t.Fatalf("event update --duration 0s: code = %q, want invalid_input", payload.Code)
	}
}

// TestEventListJSONIncludesAttendees locks in that `event list --output json`
// hydrates attendees the same way `event get` does. Chroncal Bar RSVP, the
// participant filter, and participant search all read the list payload. They
// cannot see invitations if list omits the guest list.
func TestEventListJSONIncludesAttendees(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work", "--email", "me@example.com"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	addOut, _, err := runChroncalCommand(t,
		"event", "add", "Sync",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
		"--attendee", "Me <me@example.com>",
		"--attendee", "Alice <alice@example.com>",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event add: %v", err)
	}
	created := mustJSONEvent(t, addOut)

	listOut, _, err := runChroncalCommand(t,
		"event", "list",
		"--from", "2026-04-21",
		"--to", "2026-04-21",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list --output json: %v", err)
	}
	var listed []jsonEvent
	if jerr := json.Unmarshal([]byte(listOut), &listed); jerr != nil {
		t.Fatalf("decode event list json: %v\n%s", jerr, listOut)
	}
	if len(listed) != 1 {
		t.Fatalf("got %d events, want 1\n%s", len(listed), listOut)
	}
	got := listed[0]
	if got.ID != created.ID {
		t.Fatalf("listed id = %d, want %d", got.ID, created.ID)
	}
	me, alice := attendeeByEmail(got.Attendees, "me@example.com"), attendeeByEmail(got.Attendees, "alice@example.com")
	if me == nil {
		t.Fatalf("list omitted owner attendee: %s", listOut)
	}
	if alice == nil {
		t.Fatalf("list omitted alice attendee: %s", listOut)
	}
	if me.Organizer {
		t.Fatalf("owner listed as organizer: %#v", me)
	}
	if me.RSVPStatus != "NEEDS-ACTION" {
		t.Fatalf("owner rsvp_status = %q, want NEEDS-ACTION", me.RSVPStatus)
	}

	getOut, _, err := runChroncalCommand(t, "event", "get", created.UID, "--output", "json")
	if err != nil {
		t.Fatalf("event get: %v", err)
	}
	fetched := mustJSONEvent(t, getOut)
	if len(got.Attendees) != len(fetched.Attendees) {
		t.Fatalf("list attendees = %#v, get attendees = %#v", got.Attendees, fetched.Attendees)
	}
}

// TestEventListJSONIncludesAttendeesOnGeneratedOccurrence locks in that a
// generated RRULE instance in `event list` still carries the master's guests.
// The bar opens those rows for RSVP; they keep the master id, not a get of a
// distinct occurrence row.
func TestEventListJSONIncludesAttendeesOnGeneratedOccurrence(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work", "--email", "me@example.com"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Weekly Sync",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--time", "09:00",
		"--duration", "30m",
		"--recurrence-rule", "FREQ=WEEKLY",
		"--attendee", "Me <me@example.com>",
		"--attendee", "Alice <alice@example.com>",
		"--output", "json",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	listOut, _, err := runChroncalCommand(t,
		"event", "list",
		"--from", "2026-04-28",
		"--to", "2026-04-28",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list --output json: %v", err)
	}
	var listed []jsonEvent
	if jerr := json.Unmarshal([]byte(listOut), &listed); jerr != nil {
		t.Fatalf("decode event list json: %v\n%s", jerr, listOut)
	}
	if len(listed) != 1 {
		t.Fatalf("got %d events, want 1 generated occurrence\n%s", len(listed), listOut)
	}
	got := listed[0]
	if got.Title != "Weekly Sync" {
		t.Fatalf("title = %q, want Weekly Sync", got.Title)
	}
	if got.StartTime != "2026-04-28T09:00:00Z" {
		t.Fatalf("start_time = %q, want generated occurrence 2026-04-28T09:00:00Z", got.StartTime)
	}
	if attendeeByEmail(got.Attendees, "me@example.com") == nil {
		t.Fatalf("generated occurrence omitted owner attendee: %s", listOut)
	}
	if attendeeByEmail(got.Attendees, "alice@example.com") == nil {
		t.Fatalf("generated occurrence omitted alice attendee: %s", listOut)
	}
}

func addOverrideGapSeries(t *testing.T) jsonEvent {
	t.Helper()
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	addOut, _, err := runChroncalCommand(t,
		"event", "add", "Override Gap",
		"--calendar", "Work",
		"--date", "2026-09-01",
		"--time", "10:00",
		"--duration", "1h",
		"--rrule", "FREQ=DAILY;COUNT=3",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event add: %v", err)
	}
	return mustJSONEvent(t, addOut)
}

// TestEventUpdate_RecurrenceIDCreatesOverride is the issue #612 repro:
// event update <series-uid> --recurrence-id <day-2> must create an override
// when no override row exists yet. The master title stays unchanged.
func TestEventUpdate_RecurrenceIDCreatesOverride(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	master := addOverrideGapSeries(t)
	day2 := "2026-09-02T10:00:00Z"

	updateOut, _, err := runChroncalCommand(t,
		"event", "update", master.UID,
		"--recurrence-id", day2,
		"--title", "Day 2 moved",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event update --recurrence-id: %v", err)
	}
	override := mustJSONEvent(t, updateOut)
	if override.Title != "Day 2 moved" {
		t.Fatalf("override title = %q, want Day 2 moved", override.Title)
	}
	if override.RecurrenceID != day2 {
		t.Fatalf("override recurrence_id = %q, want %q", override.RecurrenceID, day2)
	}
	if override.UID != master.UID {
		t.Fatalf("override uid = %q, want master uid %q", override.UID, master.UID)
	}

	getMaster, _, err := runChroncalCommand(t, "event", "get", master.UID, "--output", "json")
	if err != nil {
		t.Fatalf("event get master: %v", err)
	}
	fresh := mustJSONEvent(t, getMaster)
	if fresh.Title != "Override Gap" {
		t.Fatalf("master title = %q, want Override Gap", fresh.Title)
	}
	if fresh.RecurrenceID != "" {
		t.Fatalf("master recurrence_id = %q, want empty", fresh.RecurrenceID)
	}
}

// TestEventGet_RecurrenceIDShowsGeneratedOccurrence locks in that get of a
// series UID plus a recurrence-id with no override row shows the generated
// occurrence. It must not report that the series does not exist.
func TestEventGet_RecurrenceIDShowsGeneratedOccurrence(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	master := addOverrideGapSeries(t)
	day2 := "2026-09-02T10:00:00Z"

	getOut, _, err := runChroncalCommand(t,
		"event", "get", master.UID,
		"--recurrence-id", day2,
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event get --recurrence-id: %v", err)
	}
	got := mustJSONEvent(t, getOut)
	if strings.Contains(getOut, "not found") {
		t.Fatalf("event get --recurrence-id reported not found:\n%s", getOut)
	}
	if got.Title != "Override Gap" {
		t.Fatalf("title = %q, want Override Gap", got.Title)
	}
	if got.StartTime != day2 {
		t.Fatalf("start_time = %q, want %q", got.StartTime, day2)
	}
	if got.EndTime != "2026-09-02T11:00:00Z" {
		t.Fatalf("end_time = %q, want 2026-09-02T11:00:00Z", got.EndTime)
	}
	if got.RecurrenceID != day2 {
		t.Fatalf("recurrence_id = %q, want %q", got.RecurrenceID, day2)
	}
	if got.UID != master.UID {
		t.Fatalf("uid = %q, want %q", got.UID, master.UID)
	}
}

func TestEventUpdate_RecurrenceIDUpdatesExistingOverride(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	master := addOverrideGapSeries(t)
	day2 := "2026-09-02T10:00:00Z"

	if _, _, err := runChroncalCommand(t,
		"event", "update", master.UID,
		"--recurrence-id", day2,
		"--title", "Day 2 moved",
	); err != nil {
		t.Fatalf("first event update --recurrence-id: %v", err)
	}

	updateOut, _, err := runChroncalCommand(t,
		"event", "update", master.UID,
		"--recurrence-id", day2,
		"--title", "Day 2 moved again",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("second event update --recurrence-id: %v", err)
	}
	override := mustJSONEvent(t, updateOut)
	if override.Title != "Day 2 moved again" {
		t.Fatalf("override title = %q, want Day 2 moved again", override.Title)
	}
	if override.RecurrenceID != day2 {
		t.Fatalf("override recurrence_id = %q, want %q", override.RecurrenceID, day2)
	}

	getMaster, _, err := runChroncalCommand(t, "event", "get", master.UID, "--output", "json")
	if err != nil {
		t.Fatalf("event get master: %v", err)
	}
	fresh := mustJSONEvent(t, getMaster)
	if fresh.Title != "Override Gap" {
		t.Fatalf("master title = %q, want Override Gap", fresh.Title)
	}

	listed, _, err := runChroncalCommand(t,
		"event", "list",
		"--from", "2026-09-01",
		"--to", "2026-09-03",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list: %v", err)
	}
	var events []jsonEvent
	if jerr := json.Unmarshal([]byte(listed), &events); jerr != nil {
		t.Fatalf("decode list: %v\n%s", jerr, listed)
	}
	var overrideCount int
	for _, e := range events {
		if e.RecurrenceID == day2 {
			overrideCount++
			if e.Title != "Day 2 moved again" {
				t.Fatalf("listed override title = %q, want Day 2 moved again", e.Title)
			}
		}
	}
	if overrideCount != 1 {
		t.Fatalf("listed overrides for %s = %d, want 1\n%s", day2, overrideCount, listed)
	}
}

func TestEventUpdate_RecurrenceIDRejectsNonOccurrence(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	master := addOverrideGapSeries(t)
	missing := "2026-12-25T10:00:00Z"

	_, stderr, err := runChroncalCommand(t,
		"event", "update", master.UID,
		"--recurrence-id", missing,
		"--title", "Not a day",
	)
	if err == nil {
		t.Fatal("event update of a non-occurrence should fail")
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") {
		t.Fatalf("error = %q, must not claim the series does not exist", msg)
	}
	if !strings.Contains(msg, "occurrence") && !strings.Contains(stderr, "occurrence") {
		t.Fatalf("error = %q stderr=%q, want a clear non-occurrence message", msg, stderr)
	}

	listed, _, lerr := runChroncalCommand(t,
		"event", "list",
		"--from", "2026-12-25",
		"--to", "2026-12-25",
		"--output", "json",
	)
	if lerr != nil {
		t.Fatalf("event list: %v", lerr)
	}
	if strings.Contains(listed, "Not a day") {
		t.Fatalf("non-occurrence update created an override:\n%s", listed)
	}
}

// TestEventRestoreNotFoundIsTagged guards the error taxonomy of the restore
// command. A live or missing event surfaced as a generic error code. The
// not-found case must report not_found like event get, so --output json
// consumers can dispatch on the code.
func TestEventRestoreNotFoundIsTagged(t *testing.T) {
	setupCalendarCLITestEnv(t)

	for _, ref := range []string{"999", "ghost-uid@example.com"} {
		_, stderr, err := runChroncalCommand(t, "event", "restore", ref, "--output", "json")
		if err == nil {
			t.Fatalf("event restore %s should fail", ref)
		}
		var payload struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
			t.Fatalf("event restore %s: decode error payload %q: %v", ref, stderr, jerr)
		}
		if payload.Code != "not_found" {
			t.Fatalf("event restore %s: code = %q, want not_found", ref, payload.Code)
		}
		if !strings.Contains(payload.Error, "not found") {
			t.Fatalf("event restore %s: error = %q, want a not-found message", ref, payload.Error)
		}
	}
}
