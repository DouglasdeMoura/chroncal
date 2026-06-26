package main

import (
	"testing"
	"time"
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
