package recurrence

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestSortExpandedEvents_LocalDayBucket guards against issue #126. The day
// bucket used for order must reflect the host's LOCAL calendar day, not
// UTC midnight. Two timed instances on the same local day that straddle UTC
// midnight group together. The all-day event of that day sorts first. An
// all-day event is a floating date (issue #875). Its day is its stored
// date, not the local day of its stored instant.
func TestSortExpandedEvents_LocalDayBucket(t *testing.T) {
	// Pin the local zone to UTC-5 for the duration of the test so the bug is
	// observable regardless of the host's real timezone.
	orig := time.Local
	time.Local = time.FixedZone("UTC-5", -5*60*60)
	t.Cleanup(func() { time.Local = orig })

	// The local day 2025-12-31 (UTC-5) holds three instances:
	//   - all-day: 2025-12-31 00:00 UTC (the stored date)
	//   - morning: 2025-12-31 15:00 UTC == 2025-12-31 10:00 local
	//   - evening: 2026-01-01 02:00 UTC == 2025-12-31 21:00 local
	// The all-day event of 2026-01-01 (stored at 2026-01-01 00:00 UTC, which
	// is 2025-12-31 19:00 local) belongs to the next day. It sorts last.
	allDay := ExpandedEvent{
		Event:        event.Event{Title: "all-day", AllDay: true},
		InstanceTime: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
	}
	nextAllDay := ExpandedEvent{
		Event:        event.Event{Title: "next all-day", AllDay: true},
		InstanceTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	morning := ExpandedEvent{
		Event:        event.Event{Title: "morning"},
		InstanceTime: time.Date(2025, 12, 31, 15, 0, 0, 0, time.UTC),
	}
	evening := ExpandedEvent{
		Event:        event.Event{Title: "evening"},
		InstanceTime: time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC),
	}

	// Start out of order so a correct sort must reorder them.
	results := []ExpandedEvent{nextAllDay, evening, morning, allDay}
	sortExpandedEvents(results)

	got := make([]string, 0, len(results))
	for _, r := range results {
		got = append(got, r.Title)
	}
	want := []string{"all-day", "morning", "evening", "next all-day"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}
