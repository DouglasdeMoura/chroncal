package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func withLocalUTC(t *testing.T) {
	t.Helper()
	prev := time.Local
	time.Local = time.UTC
	t.Cleanup(func() {
		time.Local = prev
	})
}

func TestFormatEventListVerbose_RendersTimeRailDetails(t *testing.T) {
	withLocalUTC(t)

	events := []event.Event{
		{
			Title:       "Team Standup",
			Location:    "Zoom",
			Description: "Sprint planning",
			StartTime:   time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			EndTime:     time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
		},
		{
			Title:     "Offsite",
			AllDay:    true,
			StartTime: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		},
	}

	got := FormatEventList(FormatEventListOptions{
		Events:      events,
		ShowAllDays: true,
		ShowWeekday: true,
		ShowMonth:   true,
		From:        time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		Verbose:     true,
	})

	want := "" +
		"Apr 21 Tue\n" +
		"----------\n" +
		"all day | Offsite\n" +
		"09:00   | Team Standup\n" +
		"        | Zoom\n" +
		"        | Sprint planning\n"
	if got != want {
		t.Fatalf("FormatEventList verbose output mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestFormatEventListVerbose_RendersOvernightContinuation(t *testing.T) {
	withLocalUTC(t)

	events := []event.Event{
		{
			Title:       "Maintenance window",
			Description: "API deploy + DB migration",
			StartTime:   time.Date(2026, 4, 21, 22, 0, 0, 0, time.UTC),
			EndTime:     time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		},
	}

	got := FormatEventList(FormatEventListOptions{
		Events:      events,
		ShowAllDays: true,
		ShowWeekday: true,
		ShowMonth:   true,
		From:        time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
		Verbose:     true,
	})

	want := "" +
		"Apr 21 Tue\n" +
		"----------\n" +
		"22:00   | Maintenance window (day 1/2)\n" +
		"        | API deploy + DB migration\n" +
		"        | ends Wed, Apr 22 09:00\n" +
		"\n" +
		"Apr 22 Wed\n" +
		"----------\n" +
		"00:00   | Maintenance window (day 2/2)\n" +
		"        | API deploy + DB migration\n" +
		"        | until 09:00\n"
	if got != want {
		t.Fatalf("FormatEventList verbose overnight output mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}
