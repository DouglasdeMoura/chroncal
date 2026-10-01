package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestDialogDayChanged_HonorsHiddenCalendars guards against a regression. The
// day-events popup, on a move to another day within the already loaded
// period, rebuilt its list from the unfiltered event cache. That made
// events from calendars hidden in the sidebar reappear in the popup.
//
// See issue #108.
func TestDialogDayChanged_HonorsHiddenCalendars(t *testing.T) {
	target := time.Date(2026, 6, 11, 0, 0, 0, 0, time.Local)

	visible := event.Event{
		ID:         1,
		CalendarID: 1,
		Title:      "Visible meeting",
		StartTime:  time.Date(2026, 6, 11, 9, 0, 0, 0, time.Local),
		EndTime:    time.Date(2026, 6, 11, 10, 0, 0, 0, time.Local),
	}
	hidden := event.Event{
		ID:         2,
		CalendarID: 2,
		Title:      "Hidden meeting",
		StartTime:  time.Date(2026, 6, 11, 11, 0, 0, 0, time.Local),
		EndTime:    time.Date(2026, 6, 11, 12, 0, 0, 0, time.Local),
	}

	m := Model{
		theme:    LoadTheme("", true),
		width:    100,
		height:   40,
		viewMode: viewMonth,
		calendar: NewCalendarModel(time.Date(2026, 6, 10, 0, 0, 0, 0, time.Local)),
		events:   []event.Event{visible, hidden},
		calendars: map[int64]CalendarInfo{
			1: {Name: "Personal", Color: "#7C3AED"},
			2: {Name: "Work", Color: "#a6e3a1"},
		},
		hiddenCalendars: map[int64]bool{2: true},
	}

	updated, _ := m.Update(DialogDayChangedMsg{Day: target})
	got := updated.(Model)

	for _, ev := range got.dialog.events {
		if ev.CalendarID == 2 {
			t.Fatalf("day popup includes event %q from hidden calendar %d", ev.Title, ev.CalendarID)
		}
	}
	if len(got.dialog.events) != 1 {
		t.Fatalf("expected 1 visible event in popup, got %d", len(got.dialog.events))
	}
}

// TestCalendarDaySelected_AllDayMidSpan guards against a regression. The day
// popup matched an event only when the event started on the selected day. A
// multi-day all-day event then showed "No events on this day" for every day
// of its span except the first one. The month grid showed the event on every
// day, so the popup and the grid disagreed.
//
// See issue #807.
func TestCalendarDaySelected_AllDayMidSpan(t *testing.T) {
	trip := event.Event{
		ID:         42,
		CalendarID: 1,
		Title:      "Trip",
		AllDay:     true,
		StartTime:  time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}

	m := Model{
		theme:     LoadTheme("", true),
		width:     100,
		height:    40,
		viewMode:  viewMonth,
		calendar:  NewCalendarModel(time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)),
		events:    []event.Event{trip},
		calendars: map[int64]CalendarInfo{1: {Name: "Personal", Color: "#7C3AED"}},
	}

	updated, _ := m.Update(CalendarDaySelectedMsg{Day: time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)})
	got := updated.(Model)

	if !got.dialogOpen {
		t.Fatal("expected the day popup to open")
	}
	if len(got.dialog.events) != 1 || got.dialog.events[0].ID != 42 {
		t.Fatalf("expected the all-day event in the popup for Oct 13, got %d events", len(got.dialog.events))
	}
}
