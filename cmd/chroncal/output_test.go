package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/douglasdemoura/chroncal/internal/tui"
)

func withLocalUTC(t *testing.T) {
	t.Helper()
	prev := time.Local
	time.Local = time.UTC
	t.Cleanup(func() {
		time.Local = prev
	})
}

func assertASCII(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		if r > 127 {
			t.Fatalf("output contains non-ASCII rune %q in %q", r, s)
		}
	}
}

func TestPrintEvent_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printEvent(&buf, event.Event{
		Title:       "Title\x1b]52;c;stolen\a",
		Location:    "Room\r\nB",
		Description: "Notes\x1b[31m",
		StartTime:   time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 4, 13, 0, 0, 0, time.UTC),
		Status:      "PENDING\x1b[31m",
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("printEvent output contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("printEvent output contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;stolen") {
		t.Fatalf("printEvent output contains OSC payload: %q", out)
	}
}

func TestPrintTodo_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printTodo(&buf, todo.Todo{
		Summary:     "Todo\x1b]52;c;clip\a",
		Status:      "NEEDS-ACTION\x1b[31m",
		Duration:    "PT15M\r\nInjected",
		Class:       "PRIVATE\x1b]52;c;clip\a",
		Location:    "Desk\r\nA",
		Description: "Notes\x1b[31m",
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("printTodo output contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("printTodo output contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("printTodo output contains OSC payload: %q", out)
	}
}

func TestPrintJournal_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printJournal(&buf, journal.Journal{
		Summary:     "Journal\x1b]52;c;clip\a",
		Status:      "FINAL\x1b[31m",
		Class:       "CONFIDENTIAL\r\nInjected",
		Description: "Body\x1b[31m",
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("printJournal output contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("printJournal output contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("printJournal output contains OSC payload: %q", out)
	}
}

func TestPrintEvent_UsesASCIIDetailLayout(t *testing.T) {
	withLocalUTC(t)

	var buf bytes.Buffer
	printEvent(&buf, event.Event{
		ID:          42,
		UID:         "team-standup-uid",
		Title:       "Team Standup",
		Location:    "Zoom",
		Description: "Sprint planning",
		StartTime:   time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
		CalendarID:  1,
	})

	got := buf.String()
	want := "" +
		"  Team Standup\n" +
		"    when:      Tue, Apr 21 2026 09:00 - 09:30\n" +
		"    location:  Zoom\n" +
		"    notes:     Sprint planning\n" +
		"    calendar:  1\n" +
		"    id:        42\n" +
		"    uid:       team-standup-uid\n"
	if got != want {
		t.Fatalf("printEvent output mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
	assertASCII(t, got)
}

func TestEventListAndSearch_ShareTheSameFlatFormat(t *testing.T) {
	withLocalUTC(t)

	events := []event.Event{
		{
			Title:     "Team Standup",
			StartTime: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC),
		},
		{
			Title:     "1:1 with Alex",
			StartTime: time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 4, 21, 14, 0, 0, 0, time.UTC),
		},
	}

	// Both `event list` and `event search` invoke this with the same
	// options. Locking the option set guards against the two commands
	// drifting apart again.
	opts := tui.FormatEventListOptions{
		Events:      events,
		ShowAllDays: false,
		ShowMonth:   true,
	}
	got := tui.FormatEventList(opts)
	want := "" +
		"Apr 21 09:00-09:30  Team Standup\n" +
		"       13:00-14:00  1:1 with Alex\n"
	if got != want {
		t.Fatalf("unified event list format mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestPrintTodo_UsesASCIIDetailLayout(t *testing.T) {
	withLocalUTC(t)

	var buf bytes.Buffer
	printTodo(&buf, todo.Todo{
		ID:         77,
		UID:        "todo-send-invoice",
		Summary:    "Send invoice",
		Status:     "NEEDS-ACTION",
		DueDate:    "2026-04-24",
		Priority:   3,
		CalendarID: 1,
	})

	got := buf.String()
	want := "" +
		"  [ ] Send invoice\n" +
		"    status:    NEEDS-ACTION\n" +
		"    due:       Fri, Apr 24 2026\n" +
		"    priority:  3\n" +
		"    calendar:  1\n" +
		"    id:        77\n" +
		"    uid:       todo-send-invoice\n"
	if got != want {
		t.Fatalf("printTodo output mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
	assertASCII(t, got)
}

func TestPrintJournal_UsesASCIIDetailLayout(t *testing.T) {
	withLocalUTC(t)

	var buf bytes.Buffer
	printJournal(&buf, journal.Journal{
		ID:         19,
		UID:        "journal-sprint-notes",
		Summary:    "Sprint notes",
		Status:     "FINAL",
		StartDate:  "2026-04-21",
		Categories: "work, sprint",
		CalendarID: 1,
	})

	got := buf.String()
	want := "" +
		"  Sprint notes\n" +
		"    date:      Tue, Apr 21 2026\n" +
		"    status:    FINAL\n" +
		"    tags:      work, sprint\n" +
		"    calendar:  1\n" +
		"    id:        19\n" +
		"    uid:       journal-sprint-notes\n"
	if got != want {
		t.Fatalf("printJournal output mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
	assertASCII(t, got)
}

func TestPrintCalendar_UsesASCIIDetailLayout(t *testing.T) {
	var buf bytes.Buffer
	printCalendar(&buf, calendar.Calendar{
		ID:          1,
		Name:        "Work",
		Color:       "#4f86f7",
		Description: "Team calendar",
	})

	got := buf.String()
	want := "" +
		"  Work\n" +
		"    color:        #4f86f7\n" +
		"    description:  Team calendar\n" +
		"    id:           1\n"
	if got != want {
		t.Fatalf("printCalendar output mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
	assertASCII(t, got)
}

func TestPrintTodo_UsesCompletedCheckbox(t *testing.T) {
	var buf bytes.Buffer
	printTodo(&buf, todo.Todo{
		Summary:     "Ship release",
		Status:      "COMPLETED",
		CompletedAt: "2026-04-24T10:15:00Z",
	})

	got := buf.String()
	if !strings.HasPrefix(got, "  [x] Ship release\n") {
		t.Fatalf("completed todo should use [x], got %q", got)
	}
	assertASCII(t, got)
}

func TestPrintTextOutputDoesNotEmitLegacyGlyphs(t *testing.T) {
	withLocalUTC(t)

	var eventBuf bytes.Buffer
	printEvent(&eventBuf, event.Event{
		Title:     "Meeting",
		StartTime: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	})

	// Detail output (event get / update) must stay ASCII-only. The list
	// path uses tui.FormatEventList, which also renders ASCII hyphens
	// between times — that surface is tested separately.
	got := eventBuf.String()
	legacyGlyphs := []string{"●", "◆", "…", "→", "–", "─", "✓", "✗", "○", "♪", "@"}
	for _, glyph := range legacyGlyphs {
		if strings.Contains(got, glyph) {
			t.Fatalf("event detail output contains legacy glyph %q in %q", glyph, got)
		}
	}
	assertASCII(t, got)
}
