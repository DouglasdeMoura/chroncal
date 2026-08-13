package model

import (
	"testing"
	"time"
)

func TestParseAbsoluteTime(t *testing.T) {
	t.Run("iCal UTC", func(t *testing.T) {
		got, err := ParseAbsoluteTime("20260401T120000Z", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("RFC 3339", func(t *testing.T) {
		got, err := ParseAbsoluteTime("2026-04-01T12:00:00Z", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("floating with timezone", func(t *testing.T) {
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Skipf("tz database unavailable: %v", err)
		}
		got, err := ParseAbsoluteTime("20260401T120000", "America/New_York")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, 4, 1, 12, 0, 0, 0, loc)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
		if got.Location().String() != loc.String() {
			t.Errorf("location = %q, want %q", got.Location(), loc)
		}
	})

	t.Run("floating without timezone keeps zero offset", func(t *testing.T) {
		got, err := ParseAbsoluteTime("20260401T120000", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Year() != 2026 || got.Hour() != 12 {
			t.Errorf("unexpected parsed time: %v", got)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		if _, err := ParseAbsoluteTime("not-a-time", ""); err == nil {
			t.Error("expected error for invalid input, got nil")
		}
	})
}

// Related only means anything on a duration trigger — RFC 5545 forbids it on
// absolute triggers and export omits it there, so import round-trips an
// absolute alarm's Related as the default START. If ContentEqual compared
// Related for absolute triggers, a stored pre-normalization "END" would break
// the ReplaceAlarms match on the next pull and re-fire an already-acknowledged
// reminder (reachable when the alarm UID does not round-trip).
func TestContentEqual_RelatedIgnoredOnAbsoluteTriggers(t *testing.T) {
	a := Alarm{Action: "DISPLAY", TriggerValue: "20260401T100000Z", Related: "END"}
	b := Alarm{Action: "DISPLAY", TriggerValue: "20260401T100000Z", Related: "START"}
	if !a.ContentEqual(b) {
		t.Error("absolute-trigger alarms differing only in the inert Related must match, or acknowledged state is lost on re-import")
	}

	c := Alarm{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "END"}
	d := Alarm{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "START"}
	if c.ContentEqual(d) {
		t.Error("duration-trigger alarms anchored to different ends fire at different times and must NOT match")
	}
}
