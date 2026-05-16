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

func TestFormatEventList_CompactCanShowEventIDAndCalendar(t *testing.T) {
	withLocalUTC(t)

	got := FormatEventList(FormatEventListOptions{
		Events: []event.Event{{
			ID:         42,
			CalendarID: 1,
			Title:      "Team Standup",
			StartTime:  time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			EndTime:    time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
		}},
		CalendarNames: map[int64]string{1: "Work"},
		ShowAllDays:   true,
		ShowWeekday:   true,
		ShowMonth:     true,
		ShowID:        true,
		ShowCalendar:  true,
		From:          time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		To:            time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
	})

	want := "" +
		"Apr 21 Tue 09:00-09:30  Team Standup (42) [Work]\n"
	if got != want {
		t.Fatalf("FormatEventList compact metadata mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestFormatEventListVerbose_RendersTimeRailDetails(t *testing.T) {
	withLocalUTC(t)

	events := []event.Event{
		{
			ID:          42,
			UID:         "team-standup-uid",
			CalendarID:  1,
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
		Events:        events,
		ShowAllDays:   true,
		ShowWeekday:   true,
		ShowMonth:     true,
		CalendarNames: map[int64]string{1: "Work"},
		ShowID:        true,
		ShowCalendar:  true,
		From:          time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		To:            time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		Verbose:       true,
	})

	want := "" +
		"Apr 21 Tue\n" +
		"----------\n" +
		"all day | Offsite\n" +
		"09:00   | Team Standup (42)\n" +
		"        | Zoom\n" +
		"        | Sprint planning\n" +
		"        | Calendar: Work\n"
	if got != want {
		t.Fatalf("FormatEventList verbose output mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestFormatEventListVerbose_RendersOvernightContinuation(t *testing.T) {
	withLocalUTC(t)

	events := []event.Event{
		{
			ID:          55,
			UID:         "maintenance-window-uid",
			CalendarID:  2,
			Title:       "Maintenance window",
			Description: "API deploy + DB migration",
			StartTime:   time.Date(2026, 4, 21, 22, 0, 0, 0, time.UTC),
			EndTime:     time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		},
	}

	got := FormatEventList(FormatEventListOptions{
		Events:        events,
		ShowAllDays:   true,
		ShowWeekday:   true,
		ShowMonth:     true,
		CalendarNames: map[int64]string{2: "Ops"},
		ShowID:        true,
		ShowCalendar:  true,
		From:          time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		To:            time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
		Verbose:       true,
	})

	want := "" +
		"Apr 21 Tue\n" +
		"----------\n" +
		"22:00   | Maintenance window (55) (day 1/2)\n" +
		"        | API deploy + DB migration\n" +
		"        | Calendar: Ops\n" +
		"        | ends Wed, Apr 22 09:00\n" +
		"\n" +
		"Apr 22 Wed\n" +
		"----------\n" +
		"00:00   | Maintenance window (55) (day 2/2)\n" +
		"        | API deploy + DB migration\n" +
		"        | Calendar: Ops\n" +
		"        | until 09:00\n"
	if got != want {
		t.Fatalf("FormatEventList verbose overnight output mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}
