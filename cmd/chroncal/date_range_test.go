package main

import (
	"strings"
	"testing"
	"time"

	// Embed the timezone database so the re-executed helper subprocess can
	// resolve a fixed non-UTC zone regardless of the host's tzdata.
	_ "time/tzdata"
)

// TestParseDateRangeFromBeyondDefaultWindow guards against issue #111:
// when --to is omitted and --from is more than 30 days in the future, the
// default `to` must follow `from` (from+30), not stay anchored to today+30,
// which would produce an inverted, silently-empty range.
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

// TestParseListDateRangeNoFlagsIsOpen guards issue #304: with neither --from
// nor --to, the retrospective todo/journal lists must use an open (zero) range
// so overdue todos and past journal entries are not filtered out.
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
// once any flag is set: a half-open zero `to` would make recurrence expansion
// (which appends only expanded instances, never masters) drop recurring
// todos/journals entirely. Setting --from must yield a finite forward window.
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

// TestParseExportDateBoundsOnlyFrom guards issue #358: supplying only --from
// must leave the upper bound open (zero), not default it to from+30 days.
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

// TestParseExportDateBoundsOnlyTo guards issue #358: supplying only --to must
// leave the lower bound open (zero), not default it to today.
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
