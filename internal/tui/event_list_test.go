package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
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

func verboseColorTestOptions(color bool) FormatEventListOptions {
	return FormatEventListOptions{
		Events: []event.Event{
			{
				ID:          42,
				CalendarID:  1,
				Title:       "Team Standup",
				Location:    "Zoom",
				Description: "Sprint planning\nRetro",
				StartTime:   time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
				EndTime:     time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
			},
			{
				Title:     "Offsite",
				AllDay:    true,
				StartTime: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
				EndTime:   time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
			},
		},
		CalendarNames: map[int64]string{1: "Work"},
		ShowWeekday:   true,
		ShowMonth:     true,
		ShowCalendar:  true,
		Verbose:       true,
		Color:         color,
	}
}

func TestFormatEventListVerbose_ColorFalseIsPlain(t *testing.T) {
	withLocalUTC(t)

	got := FormatEventList(verboseColorTestOptions(false))
	want := "" +
		"Apr 21 Tue\n" +
		"----------\n" +
		"all day | Offsite\n" +
		"09:00   | Team Standup\n" +
		"        | Zoom\n" +
		"        | Sprint planning\n" +
		"        | Retro\n" +
		"        | Calendar: Work\n"
	if got != want {
		t.Fatalf("FormatEventList verbose plain mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestFormatEventListVerbose_ColorTrueAddsSGRAndKeepsAlignment(t *testing.T) {
	withLocalUTC(t)

	got := FormatEventList(verboseColorTestOptions(true))
	want := "" +
		"\x1b[1;36mApr 21 Tue\x1b[0m\n" +
		"\x1b[1;36m----------\x1b[0m\n" +
		"\x1b[33mall day\x1b[0m | \x1b[1mOffsite\x1b[0m\n" +
		"\x1b[33m09:00  \x1b[0m | \x1b[1mTeam Standup\x1b[0m\n" +
		"        | Zoom\n" +
		"        | \x1b[2mSprint planning\x1b[0m\n" +
		"        | \x1b[2mRetro\x1b[0m\n" +
		"        | \x1b[35mCalendar: Work\x1b[0m\n"
	if got != want {
		t.Fatalf("FormatEventList verbose color mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}

	// The color must not change the layout: with the SGR codes removed,
	// the output is the same as the plain output.
	plain := FormatEventList(verboseColorTestOptions(false))
	if stripped := textsafe.StripEscapes(got); stripped != plain {
		t.Fatalf("color output without SGR codes differs from plain output\nwant:\n%q\ngot:\n%q", plain, stripped)
	}
}

func TestFormatEventListCompact_IgnoresColor(t *testing.T) {
	withLocalUTC(t)

	opts := verboseColorTestOptions(true)
	opts.Verbose = false
	got := FormatEventList(opts)
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("compact FormatEventList output has SGR codes: %q", got)
	}
}

func TestFormatEventListVerbose_PrefixesEveryDescriptionLine(t *testing.T) {
	withLocalUTC(t)

	got := FormatEventList(FormatEventListOptions{
		Events: []event.Event{{
			Title:       "Review",
			Description: "Agenda:\r\n- item one\n\n- item two\n",
			StartTime:   time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			EndTime:     time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
		}},
		ShowMonth: true,
		Verbose:   true,
	})

	want := "" +
		"Apr 21\n" +
		"------\n" +
		"09:00   | Review\n" +
		"        | Agenda:\n" +
		"        | - item one\n" +
		"        |\n" +
		"        | - item two\n"
	if got != want {
		t.Fatalf("FormatEventList verbose multi-line description mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestFormatEventListVerbose_SanitizesText(t *testing.T) {
	withLocalUTC(t)

	got := FormatEventList(FormatEventListOptions{
		Events: []event.Event{{
			Title:       "Evil\x1b[31m title\u202e",
			Location:    "Room\x1b]0;pwned\x07 7\tB",
			Description: "line\x1b[2J one\nline\x07 two",
			StartTime:   time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			EndTime:     time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
		}},
		ShowMonth: true,
		Verbose:   true,
	})

	want := "" +
		"Apr 21\n" +
		"------\n" +
		"09:00   | Evil title\n" +
		"        | Room 7 B\n" +
		"        | line one\n" +
		"        | line two\n"
	if got != want {
		t.Fatalf("FormatEventList verbose sanitize mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

// TestFormatEventListCompactSanitizesText keeps escape sequences of a remote
// title and of a calendar name out of the default (not verbose) list. A
// remote event can carry an ANSI or OSC sequence that changes the terminal.
func TestFormatEventListCompactSanitizesText(t *testing.T) {
	withLocalUTC(t)

	got := FormatEventList(FormatEventListOptions{
		Events: []event.Event{{
			ID:         7,
			CalendarID: 1,
			Title:      "Standup\x1b]0;owned\x07\x1b[31m",
			StartTime:  time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			EndTime:    time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
		}},
		CalendarNames: map[int64]string{1: "Work\x1b[2J"},
		ShowAllDays:   true,
		ShowMonth:     true,
		ShowCalendar:  true,
		From:          time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		To:            time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
	})
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("compact list keeps a control character:\n%q", got)
	}
	if !strings.Contains(got, "Standup") || !strings.Contains(got, "Work") {
		t.Fatalf("compact list lost the text:\n%q", got)
	}
}
