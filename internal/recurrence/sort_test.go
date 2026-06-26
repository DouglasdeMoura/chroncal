package recurrence

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestSortExpandedEvents_LocalDayBucket guards against issue #126: the day
// bucket used for ordering must reflect the host's LOCAL calendar day, not
// UTC midnight. Two instances on the same local day that straddle UTC midnight
// must group together, with the all-day event ordered before the timed one.
func TestSortExpandedEvents_LocalDayBucket(t *testing.T) {
	// Pin the local zone to UTC-5 for the duration of the test so the bug is
	// observable regardless of the host's real timezone.
	orig := time.Local
	time.Local = time.FixedZone("UTC-5", -5*60*60)
	t.Cleanup(func() { time.Local = orig })

	// Both instances fall on the local calendar day 2025-12-31 (UTC-5), but
	// they sit on opposite sides of UTC midnight:
	//   - all-day: 2026-01-01 00:00 UTC == 2025-12-31 19:00 local (stored as
	//     midnight UTC, the canonical all-day representation)
	//   - timed:   2025-12-31 15:00 UTC == 2025-12-31 10:00 local
	// A UTC-midnight bucket (the bug) buckets the all-day event on Jan 1 and
	// the timed event on Dec 31, so the timed event sorts first — wrong. A
	// local-day bucket keeps them on Dec 31 with the all-day event first.
	allDay := ExpandedEvent{
		Event:        event.Event{Title: "all-day", AllDay: true},
		InstanceTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	timed := ExpandedEvent{
		Event:        event.Event{Title: "timed"},
		InstanceTime: time.Date(2025, 12, 31, 15, 0, 0, 0, time.UTC),
	}

	// Start out of order so a correct sort must reorder them.
	results := []ExpandedEvent{timed, allDay}
	sortExpandedEvents(results)

	if results[0].Title != "all-day" || results[1].Title != "timed" {
		t.Fatalf("expected all-day before timed on the same local day, got [%s, %s]",
			results[0].Title, results[1].Title)
	}
}
