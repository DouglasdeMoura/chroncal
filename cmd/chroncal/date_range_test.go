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
