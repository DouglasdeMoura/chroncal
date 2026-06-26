package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestBuildAgendaRows_AllDayNegativeOffset guards against the agenda view
// placing all-day events on the wrong day for negative-UTC-offset timezones.
// All-day events are stored as midnight-UTC datestamps; converting them with
// .Local() in a UTC-7 zone rolls them back to the previous day. The agenda
// must use the UTC date, matching the month/week/day grids.
func TestBuildAgendaRows_AllDayNegativeOffset(t *testing.T) {
	orig := time.Local
	time.Local = time.FixedZone("US/Pacific", -7*60*60)
	defer func() { time.Local = orig }()

	// All-day event on 2026-04-17 (stored at midnight UTC).
	ev := event.Event{
		ID:        1,
		Title:     "Conference",
		AllDay:    true,
		StartTime: time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC),
	}

	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local)
	rows := buildAgendaRows([]event.Event{ev}, start, 7, false)

	var gotDays []string
	for _, r := range rows {
		if r.event.ID == ev.ID {
			gotDays = append(gotDays, r.day.Format("2006-01-02"))
		}
	}
	if len(gotDays) != 1 || gotDays[0] != "2026-04-17" {
		t.Fatalf("all-day event placed on %v, want exactly [2026-04-17]", gotDays)
	}
}
