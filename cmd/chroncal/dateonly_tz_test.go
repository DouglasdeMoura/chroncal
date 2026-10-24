package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestAllDayEventOutput_EveryZone guards issue #875. The CLI shows the
// dates of an all-day event in every zone. A conversion with .Local()
// showed the day before, west of UTC.
func TestAllDayEventOutput_EveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		single := event.Event{
			Title: "Holiday", AllDay: true,
			StartTime: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		}
		multi := event.Event{
			Title: "Trip", AllDay: true,
			StartTime: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC),
		}

		if date, tm := compactEventDateColumns(single); date != "2026-10-28" || tm != "all-day" {
			t.Fatalf("compact single = %q %q, want 2026-10-28 all-day", date, tm)
		}
		if date, _ := compactEventDateColumns(multi); date != "2026-11-02/2026-11-06" {
			t.Fatalf("compact multi = %q, want 2026-11-02/2026-11-06", date)
		}

		var buf bytes.Buffer
		printEvent(&buf, single)
		if !strings.Contains(buf.String(), "Wed, Oct 28 2026 (all day)") {
			t.Fatalf("event detail:\n%s\nwant Wed, Oct 28 2026 (all day)", buf.String())
		}
		buf.Reset()
		printEvent(&buf, multi)
		if !strings.Contains(buf.String(), "Mon, Nov 2 2026 to Fri, Nov 6 2026 (all day)") {
			t.Fatalf("event detail:\n%s\nwant Mon, Nov 2 2026 to Fri, Nov 6 2026 (all day)", buf.String())
		}
		buf.Reset()
		printEventDetail(&buf, multi, false)
		if !strings.Contains(buf.String(), "all day (Mon, Nov 2 2026 to Fri, Nov 6 2026)") {
			t.Fatalf("event list detail:\n%s\nwant all day (Mon, Nov 2 2026 to Fri, Nov 6 2026)", buf.String())
		}
	})
}
