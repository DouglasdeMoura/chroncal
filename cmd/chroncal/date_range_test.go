package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	// Embed the timezone database so the re-executed helper subprocess can
	// resolve a fixed non-UTC zone regardless of the host's tzdata.
	_ "time/tzdata"
)

// TestParseDateRangeFromBeyondDefaultWindow guards against issue #111.
// When --to is omitted and --from is more than 30 days in the future, the
// default `to` must follow `from` (from+30). It must not stay anchored to
// today+30. That would produce an inverted, empty range in silence.
func TestParseDateRangeFromBeyondDefaultWindow(t *testing.T) {
	now := time.Now()
	fromStr := now.AddDate(0, 0, 60).Format("2006-01-02")

	from, to, err := parseDateRange(fromStr, "")
	if err != nil {
		t.Fatalf("parseDateRange returned error: %v", err)
	}
	if !to.After(from) {
		t.Fatalf("expected to (%s) to be after from (%s); inverted range", to, from)
	}
}

func TestParseDateRangeDefaultToFollowsFrom(t *testing.T) {
	from, to, err := parseDateRange("2026-09-01", "")
	if err != nil {
		t.Fatalf("parseDateRange returned error: %v", err)
	}
	wantTo := from.AddDate(0, 0, 30)
	if !to.Equal(wantTo) {
		t.Fatalf("default to = %s, want %s (from+30)", to, wantTo)
	}
}

// TestParseListDateRangeNoFlagsIsOpen guards issue #304. With neither --from
// nor --to, the retrospective todo/journal lists must use an open (zero) range.
// Overdue todos and past journal entries are then not filtered out.
func TestParseListDateRangeNoFlagsIsOpen(t *testing.T) {
	from, to, err := parseListDateRange("", "")
	if err != nil {
		t.Fatalf("parseListDateRange returned error: %v", err)
	}
	if !from.IsZero() || !to.IsZero() {
		t.Fatalf("no-flags range = [%s, %s), want both zero (open)", from, to)
	}
}

// TestParseListDateRangeWithFlagIsFinite guards against an open upper bound
// once any flag is set. A half-open zero `to` would make recurrence expansion
// (which appends only expanded instances, never masters) drop recurring
// todos/journals entirely. A set of --from must yield a finite forward window.
func TestParseListDateRangeWithFlagIsFinite(t *testing.T) {
	from, to, err := parseListDateRange("2026-09-01", "")
	if err != nil {
		t.Fatalf("parseListDateRange returned error: %v", err)
	}
	if from.IsZero() || to.IsZero() {
		t.Fatalf("range with --from = [%s, %s), want both non-zero (finite)", from, to)
	}
	if !to.After(from) {
		t.Fatalf("expected to (%s) after from (%s)", to, from)
	}
}

// TestParseExportDateBoundsOnlyFrom guards issue #358. Only --from
// must leave the upper bound open (zero). It must not default it to from+30 days.
func TestParseExportDateBoundsOnlyFrom(t *testing.T) {
	from, to, err := parseExportDateBounds("2026-01-01", "")
	if err != nil {
		t.Fatalf("parseExportDateBounds: %v", err)
	}
	if from.IsZero() {
		t.Fatal("from must not be zero when --from is given")
	}
	if !to.IsZero() {
		t.Fatalf("to must be zero (unbounded) when --to is omitted, got %s", to)
	}
}

// TestParseExportDateBoundsOnlyTo guards issue #358. Only --to must
// leave the lower bound open (zero). It must not default it to today.
func TestParseExportDateBoundsOnlyTo(t *testing.T) {
	from, to, err := parseExportDateBounds("", "2026-12-31")
	if err != nil {
		t.Fatalf("parseExportDateBounds: %v", err)
	}
	if !from.IsZero() {
		t.Fatalf("from must be zero (unbounded) when --from is omitted, got %s", from)
	}
	if to.IsZero() {
		t.Fatal("to must not be zero when --to is given")
	}
}

// TestParseExportDateBoundsBoth verifies that when both flags are present the
// returned window is non-zero and to is strictly after from.
func TestParseExportDateBoundsBoth(t *testing.T) {
	from, to, err := parseExportDateBounds("2026-04-01", "2026-04-30")
	if err != nil {
		t.Fatalf("parseExportDateBounds: %v", err)
	}
	if from.IsZero() || to.IsZero() {
		t.Fatalf("both bounds must be non-zero, got from=%s to=%s", from, to)
	}
	if !to.After(from) {
		t.Fatalf("to (%s) must be after from (%s)", to, from)
	}
}

// TestParseExportDateBoundsNeither verifies that with no flags both bounds are
// zero (unbounded).
func TestParseExportDateBoundsNeither(t *testing.T) {
	from, to, err := parseExportDateBounds("", "")
	if err != nil {
		t.Fatalf("parseExportDateBounds: %v", err)
	}
	if !from.IsZero() || !to.IsZero() {
		t.Fatalf("both bounds must be zero when no flags given, got from=%s to=%s", from, to)
	}
}

// TestParseDateRangeRejectsInvertedRange guards against a silent empty
// window. --to before --from used to be accepted; every row was then
// filtered out with no error. The same-day range stays valid because the
// half-open end bound includes the whole --to day.
func TestParseDateRangeRejectsInvertedRange(t *testing.T) {
	_, _, err := parseDateRange("2026-05-10", "2026-05-01")
	if err == nil {
		t.Fatal("parseDateRange accepted --to before --from")
	}
	var ce *cliError
	if !errors.As(err, &ce) || ce.Code != "invalid_input" {
		t.Fatalf("error = %#v, want an invalid_input cliError", err)
	}

	if _, _, err := parseDateRange("2026-05-10", "2026-05-10"); err != nil {
		t.Fatalf("same-day range must stay valid, got %v", err)
	}
	if _, _, err := parseDateRange("2026-05-10", ""); err != nil {
		t.Fatalf("default upper bound must stay valid, got %v", err)
	}
}

// TestParseExportDateBoundsRejectsInvertedRange mirrors the guard above
// for the export bounds parser. Both bounds must be set for the check to
// run; a single bound keeps its open window (issue #358).
func TestParseExportDateBoundsRejectsInvertedRange(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		wantErr  bool
	}{
		{"2026-05-10", "2026-05-01", true},  // to strictly before from
		{"2026-05-10", "2026-05-09", true},  // to one day before from
		{"2026-05-10", "2026-05-10", false}, // whole --to day included
		{"2026-05-10", "", false},           // open upper bound
		{"", "2026-05-10", false},           // open lower bound
	} {
		_, _, err := parseExportDateBounds(tc.from, tc.to)
		if tc.wantErr && err == nil {
			t.Fatalf("parseExportDateBounds(%q, %q) accepted an inverted range", tc.from, tc.to)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("parseExportDateBounds(%q, %q) = %v, want valid", tc.from, tc.to, err)
		}
	}
}

// TestICalExportOnlyFromIsUnboundedAbove guards issue #358: when only --from is
// given the export must include events beyond from+30 days (no silent upper
// bound). Before the fix, parseDateRange silently set to=from+30.
func TestICalExportOnlyFromIsUnboundedAbove(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	// This event starts more than 30 days after the --from date below.
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Far future event",
		"--calendar", "Work",
		"--date", "2026-09-01",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "ical", "export",
		"--calendar", "Work",
		"--from", "2026-06-01")
	if err != nil {
		t.Fatalf("ical export: %v", err)
	}

	if !strings.Contains(stdout, "Far future event") {
		t.Fatalf("ical export --from 2026-06-01 should include event on 2026-09-01 "+
			"(beyond the old 30-day default window) but did not\noutput:\n%s", stdout)
	}
}

// TestICalExportOnlyToIsUnboundedBelow guards issue #358: when only --to is
// given the export must include events before today (no silent lower bound at
// today). Before the fix, parseDateRange silently set from=today.
func TestICalExportOnlyToIsUnboundedBelow(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	// This event is well in the past.
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Past event",
		"--calendar", "Work",
		"--date", "2020-01-01",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "ical", "export",
		"--calendar", "Work",
		"--to", "2026-12-31")
	if err != nil {
		t.Fatalf("ical export: %v", err)
	}

	if !strings.Contains(stdout, "Past event") {
		t.Fatalf("ical export --to 2026-12-31 should include event on 2020-01-01 "+
			"(before the old today lower bound) but did not\noutput:\n%s", stdout)
	}
}
