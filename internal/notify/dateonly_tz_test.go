package notify

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/alarm"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestFormatNotification_AllDayEveryZone guards issue #875. The body of an
// all-day alarm names the date of the event in every zone.
func TestFormatNotification_AllDayEveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		_, body := FormatNotification(alarm.DueAlarm{Event: event.Event{
			Title: "Holiday", AllDay: true,
			StartTime: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		}})
		if body != "Wed Oct 28" {
			t.Fatalf("body = %q, want %q", body, "Wed Oct 28")
		}
	})
}
