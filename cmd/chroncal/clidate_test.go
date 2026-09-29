package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	// Embed the timezone database so the tests resolve fixed non-UTC zones
	// regardless of the host's tzdata.
	_ "time/tzdata"
)

// relNow anchors the weekday tests: Thursday, 2026-04-09, 20:15 UTC.
var relNow = time.Date(2026, 4, 9, 20, 15, 30, 0, time.UTC)

func TestResolveRelativeDateWords(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Time
	}{
		// Day words.
		{"today", "today", time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		{"now is the date", "now", time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		{"tomorrow", "tomorrow", time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)},
		{"yesterday", "yesterday", time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)},
		{"case-insensitive", "ToMoRrOw", time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)},

		// Weekday words. April 9, 2026 is a Thursday, and today counts as
		// an occurrence (issue #785).
		{"same weekday is today", "thursday", time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		{"same weekday short", "thu", time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		{"same weekday variants", "thurs", time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		{"next day weekday", "friday", time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)},
		{"weekday six ahead", "wednesday", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)},
		{"weekday three ahead", "sunday", time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)},
		{"weekday four ahead", "monday", time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)},
		{"weekday two ahead", "saturday", time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)},

		// "next <weekday>" is the first occurrence after today, as in GNU
		// date. It differs from the bare weekday only on that weekday.
		{"next same weekday adds a week", "next thursday", time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)},
		{"next near weekday", "next friday", time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)},
		{"next far weekday", "next wed", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)},

		// Signed offsets.
		{"plus days", "+1d", time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)},
		{"plus zero days", "+0d", time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)},
		{"minus days", "-1d", time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)},
		{"plus many days", "+30d", time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)},
		{"plus weeks", "+1w", time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)},
		{"minus weeks", "-2w", time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)},
		{"plus months", "+1m", time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)},
		{"minus months", "-1m", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)},
		{"offset case-insensitive", "+3D", time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveRelativeDate(tt.value, relNow, time.UTC)
			if !ok {
				t.Fatalf("resolveRelativeDate(%q) rejected a relative word", tt.value)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("resolveRelativeDate(%q) = %s, want %s", tt.value, got, tt.want)
			}
		})
	}
}

// TestResolveRelativeDateMonthClamp checks the calendar-month rule. Go
// normalizes January 31 plus one month to March 3, the same result GNU date
// gives for "date -d 'jan 31 +1 month'".
func TestResolveRelativeDateMonthClamp(t *testing.T) {
	now := time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)
	got, ok := resolveRelativeDate("+1m", now, time.UTC)
	if !ok {
		t.Fatal("resolveRelativeDate(+1m) rejected a relative word")
	}
	want := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("resolveRelativeDate(+1m) = %s, want %s (month-overflow normalization)", got, want)
	}
}

func TestResolveRelativeDateRejectsNonRelative(t *testing.T) {
	for _, value := range []string{
		"",                // empty
		"2026-04-09",      // absolute date stays strict
		"3d",              // offset needs a sign
		"+3",              // offset needs a unit
		"+3x",             // unknown unit
		"-w",              // offset needs a count
		"today!",          // trailing junk
		"next",            // next needs a weekday
		"next month",      // next only accepts weekdays
		"next fryday",     // misspelled weekday
		"today tomorrow",  // two words
		"+9999999999999d", // count beyond int range
	} {
		if _, ok := resolveRelativeDate(value, relNow, time.UTC); ok {
			t.Fatalf("resolveRelativeDate(%q) accepted a non-relative value", value)
		}
	}
}

// TestResolveRelativeDateAnchorsOnLocation checks that the word resolves in
// the given location, not in the location of now. At 02:30 UTC on April 9
// it is still April 8 in Sao Paulo.
func TestResolveRelativeDateAnchorsOnLocation(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 4, 9, 2, 30, 0, 0, time.UTC)

	got, ok := resolveRelativeDate("today", now, loc)
	if !ok {
		t.Fatal("resolveRelativeDate(today) rejected a relative word")
	}
	want := time.Date(2026, 4, 8, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("resolveRelativeDate(today) = %s, want %s (Sao Paulo calendar day)", got, want)
	}
	if got.Location() != loc {
		t.Fatalf("result location = %v, want %v", got.Location(), loc)
	}
}

// TestRelativeDateSkippedMidnight checks a zone that skips midnight for a
// daylight saving change. In America/Santiago, 2026-09-06 00:00 does not
// exist, and Go moves that midnight to 23:00 on September 5. The calendar
// date must not move back one day.
func TestRelativeDateSkippedMidnight(t *testing.T) {
	loc, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, loc)

	for _, tt := range []struct{ value, want string }{
		{"today", "2026-09-06"},
		{"+7d", "2026-09-13"},
		{"yesterday", "2026-09-05"},
		{"2026-09-06", "2026-09-06"},
	} {
		got, err := parseCLIDateString("due", tt.value, now, loc)
		if err != nil {
			t.Fatalf("parseCLIDateString(%q): %v", tt.value, err)
		}
		if got != tt.want {
			t.Fatalf("parseCLIDateString(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}

	// The skipped day starts at 01:00 -03 (04:00 UTC). time.Date gives
	// 23:00 on September 5 for the missing midnight. The relative path and
	// the strict YYYY-MM-DD path must both give the real start of the day.
	dayStart := time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)
	rel, err := parseCLIDate("date", "+7d", time.Date(2026, 8, 30, 12, 0, 0, 0, loc), loc)
	if err != nil {
		t.Fatalf("parseCLIDate(+7d): %v", err)
	}
	abs, err := parseCLIDate("date", "2026-09-06", now, loc)
	if err != nil {
		t.Fatalf("parseCLIDate(2026-09-06): %v", err)
	}
	for name, got := range map[string]time.Time{"+7d": rel, "2026-09-06": abs} {
		if !got.Equal(dayStart) {
			t.Fatalf("parseCLIDate(%s) = %s, want %s (start of September 6)", name, got, dayStart.In(loc))
		}
		if y, m, d := got.Date(); y != 2026 || m != time.September || d != 6 {
			t.Fatalf("parseCLIDate(%s) is on %04d-%02d-%02d, want 2026-09-06", name, y, m, d)
		}
	}

	// An inclusive end on the day before the change must stop at the
	// start of September 6, not at 23:00 on September 5.
	sep5, err := parseCLIDate("to", "2026-09-05", now, loc)
	if err != nil {
		t.Fatalf("parseCLIDate(2026-09-05): %v", err)
	}
	if got := dayAfter(sep5); !got.Equal(dayStart) {
		t.Fatalf("dayAfter(2026-09-05) = %s, want %s", got, dayStart.In(loc))
	}
	// The day after the skipped day starts at a normal midnight.
	if got, want := dayAfter(abs), time.Date(2026, 9, 7, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("dayAfter(2026-09-06) = %s, want %s", got, want)
	}
}

// TestResolveRelativeDateRejectsOutOfRangeYear checks that a relative word
// cannot resolve outside the years 0000-9999. The strict YYYY-MM-DD format
// cannot hold such a year, so a stored todo or journal date would not
// parse again.
func TestResolveRelativeDateRejectsOutOfRangeYear(t *testing.T) {
	for _, value := range []string{"+100000m", "+1000000w", "-30000m"} {
		if _, ok := resolveRelativeDate(value, relNow, time.UTC); ok {
			t.Fatalf("resolveRelativeDate(%q) accepted a year outside 0000-9999", value)
		}
		if _, err := parseCLIDateString("due", value, relNow, time.UTC); err == nil {
			t.Fatalf("parseCLIDateString(%q) accepted a year outside 0000-9999", value)
		}
	}
	// The last day of year 9999 is still in range.
	now := time.Date(9999, 12, 30, 12, 0, 0, 0, time.UTC)
	if got, err := parseCLIDateString("due", "+1d", now, time.UTC); err != nil || got != "9999-12-31" {
		t.Fatalf("parseCLIDateString(+1d) = %q, %v; want 9999-12-31", got, err)
	}
	if _, err := parseCLIDateString("due", "+2d", now, time.UTC); err == nil {
		t.Fatal("parseCLIDateString(+2d) accepted year 10000")
	}
}

// TestEventAddAllDayOnSkippedMidnight runs the built binary in a zone that
// skips midnight. An all-day event on that day must keep its date.
func TestEventAddAllDayOnSkippedMidnight(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "America/Santiago")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Holiday",
		"--calendar", "Work",
		"--date", "2026-09-06",
		"--end-date", "2026-09-06",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}
	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--from", "2026-09-06",
		"--to", "2026-09-06",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list: %v", err)
	}
	var events []struct {
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &events); jerr != nil {
		t.Fatalf("decode %q: %v", stdout, jerr)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1\noutput:\n%s", len(events), stdout)
	}
	if events[0].StartTime != "2026-09-06T00:00:00Z" || events[0].EndTime != "2026-09-07T00:00:00Z" {
		t.Fatalf("event = %s..%s, want 2026-09-06T00:00:00Z..2026-09-07T00:00:00Z", events[0].StartTime, events[0].EndTime)
	}
}

// TestParseCLIDateAnchored checks that a relative word resolves on the
// calendar day of the anchor zone and builds midnight in the target zone.
// At 20:00 on Monday in New York, it is already Tuesday in Tokyo.
func TestParseCLIDateAnchored(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 4, 13, 20, 0, 0, 0, ny)

	got, err := parseCLIDateAnchored("date", "tomorrow", now, ny, tokyo)
	if err != nil {
		t.Fatalf("parseCLIDateAnchored: %v", err)
	}
	if want := time.Date(2026, 4, 14, 0, 0, 0, 0, tokyo); !got.Equal(want) {
		t.Fatalf("parseCLIDateAnchored(tomorrow) = %s, want %s (New York tomorrow, Tokyo midnight)", got, want)
	}
}

func TestParseCLIDate(t *testing.T) {
	t.Run("absolute date still parses", func(t *testing.T) {
		got, err := parseCLIDate("date", "2026-04-09", relNow, time.UTC)
		if err != nil {
			t.Fatalf("parseCLIDate: %v", err)
		}
		if want := time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
			t.Fatalf("parseCLIDate = %s, want %s", got, want)
		}
	})
	t.Run("relative date resolves", func(t *testing.T) {
		got, err := parseCLIDate("date", "friday", relNow, time.UTC)
		if err != nil {
			t.Fatalf("parseCLIDate: %v", err)
		}
		if want := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
			t.Fatalf("parseCLIDate = %s, want %s", got, want)
		}
	})
	t.Run("invalid value names the flag and both formats", func(t *testing.T) {
		_, err := parseCLIDate("date", "thrsday", relNow, time.UTC)
		if err == nil {
			t.Fatal("parseCLIDate accepted an invalid date")
		}
		var ce *cliError
		if !errors.As(err, &ce) || ce.Code != "invalid_input" {
			t.Fatalf("error = %#v, want an invalid_input cliError", err)
		}
		for _, want := range []string{"--date", "YYYY-MM-DD", "relative"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q must mention %q", err.Error(), want)
			}
		}
	})
}

func TestParseCLIDateString(t *testing.T) {
	t.Run("relative word canonicalizes to YYYY-MM-DD", func(t *testing.T) {
		got, err := parseCLIDateString("due", "friday", relNow, time.UTC)
		if err != nil {
			t.Fatalf("parseCLIDateString: %v", err)
		}
		if got != "2026-04-10" {
			t.Fatalf("parseCLIDateString = %q, want %q", got, "2026-04-10")
		}
	})
	t.Run("empty stays empty", func(t *testing.T) {
		got, err := parseCLIDateString("due", "", relNow, time.UTC)
		if err != nil {
			t.Fatalf("parseCLIDateString: %v", err)
		}
		if got != "" {
			t.Fatalf("parseCLIDateString = %q, want empty", got)
		}
	})
	t.Run("invalid value errors", func(t *testing.T) {
		_, err := parseCLIDateString("due", "someday", relNow, time.UTC)
		if err == nil {
			t.Fatal("parseCLIDateString accepted an invalid date")
		}
	})
}

func TestParseTUIAtRelative(t *testing.T) {
	got, err := parseTUIAt("tomorrow", relNow)
	if err != nil {
		t.Fatalf("parseTUIAt: %v", err)
	}
	year, month, day := relNow.In(time.Local).Date()
	want := time.Date(year, month, day, 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	if !got.Equal(want) {
		t.Fatalf("parseTUIAt(tomorrow) = %s, want %s (next day at midnight local)", got, want)
	}
}

// cliRelativeDate computes the expected YYYY-MM-DD for a CLI subprocess
// test. The subprocess and the test process can resolve relative words on
// opposite sides of midnight. Recompute once after the run: when the two
// computations disagree, the boundary was crossed and either answer holds.
// The subprocess runs with TZ=UTC, but t.Setenv does not reload time.Local
// in the test process. The computation therefore uses UTC explicitly.
func cliRelativeDate(t *testing.T, before time.Time, days int, got string) string {
	t.Helper()
	want := before.UTC().AddDate(0, 0, days).Format("2006-01-02")
	if got == want {
		return want
	}
	if after := time.Now().UTC().AddDate(0, 0, days).Format("2006-01-02"); got == after {
		return after
	}
	return want
}

// TestEventAddAndListAcceptRelativeDates runs the built binary end to end.
// It covers the exact reproduction from issue #785: --date with a weekday
// word, and a --from today --to +7d window.
func TestEventAddAndListAcceptRelativeDates(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	before := time.Now()
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Birthday party",
		"--calendar", "Work",
		"--date", "tomorrow",
		"--time", "17:00",
	); err != nil {
		t.Fatalf("event add --date tomorrow: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"event", "list",
		"--from", "today",
		"--to", "+7d",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event list --from today --to +7d: %v", err)
	}

	var events []struct {
		StartTime string `json:"start_time"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &events); jerr != nil {
		t.Fatalf("decode %q: %v", stdout, jerr)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1\noutput:\n%s", len(events), stdout)
	}
	wantDate := cliRelativeDate(t, before, 1, strings.TrimSuffix(events[0].StartTime, "T17:00:00Z"))
	if events[0].StartTime != wantDate+"T17:00:00Z" {
		t.Fatalf("start_time = %q, want %q (tomorrow at 17:00)", events[0].StartTime, wantDate+"T17:00:00Z")
	}
}

func TestTodoAddAcceptsRelativeDueDate(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	before := time.Now()
	if _, _, err := runChroncalCommand(t,
		"todo", "add", "Ship the report",
		"--due", "friday",
	); err != nil {
		t.Fatalf("todo add --due friday: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "todo", "list", "--output", "json")
	if err != nil {
		t.Fatalf("todo list: %v", err)
	}

	var todos []struct {
		DueDate string `json:"due_date"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &todos); jerr != nil {
		t.Fatalf("decode %q: %v", stdout, jerr)
	}
	if len(todos) != 1 {
		t.Fatalf("got %d todos, want 1\noutput:\n%s", len(todos), stdout)
	}
	// The next Friday is 0 to 7 days ahead of today, so accept that span.
	got, err := time.Parse("2006-01-02", todos[0].DueDate)
	if err != nil {
		t.Fatalf("due_date %q is not a date: %v", todos[0].DueDate, err)
	}
	// The subprocess runs with TZ=UTC. Take the date components in UTC, not
	// in the time.Local value of the test process.
	year, month, day := before.UTC().Date()
	todayBefore := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	days := got.Sub(todayBefore).Hours() / 24
	if days < 0 || days > 7 {
		t.Fatalf("due_date %q is %.0f days ahead, want 0-7 (next Friday)", todos[0].DueDate, days)
	}
	if got.Weekday() != time.Friday {
		t.Fatalf("due_date %q is a %s, want Friday", todos[0].DueDate, got.Weekday())
	}
}

func TestJournalAddAcceptsRelativeDate(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	before := time.Now()
	if _, _, err := runChroncalCommand(t,
		"journal", "add", "Standup notes",
		"--date", "today",
	); err != nil {
		t.Fatalf("journal add --date today: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "journal", "list", "--output", "json")
	if err != nil {
		t.Fatalf("journal list: %v", err)
	}

	var entries []struct {
		Date string `json:"start_date"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &entries); jerr != nil {
		t.Fatalf("decode %q: %v", stdout, jerr)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d journal entries, want 1\noutput:\n%s", len(entries), stdout)
	}
	wantDate := cliRelativeDate(t, before, 0, entries[0].Date)
	if entries[0].Date != wantDate {
		t.Fatalf("start_date = %q, want %q (today)", entries[0].Date, wantDate)
	}
}

func TestRelativeDateErrorNamesBothFormats(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t,
		"event", "list",
		"--from", "thrsday",
	)
	if err == nil {
		t.Fatal("event list accepted an invalid relative date")
	}
	if !strings.Contains(stderr, "--from") {
		t.Fatalf("error must mention the flag, got: %s", stderr)
	}
	if !strings.Contains(stderr, "relative") {
		t.Fatalf("error must mention the relative vocabulary, got: %s", stderr)
	}
}
