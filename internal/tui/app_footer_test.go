package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestModel_CurrentFooterShowsTodayHint(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)

	m := Model{
		viewMode: viewDay,
		focus:    focusCalendar,
		day: DayModel{
			cursor: today.AddDate(0, 0, 1),
			today:  today,
		},
	}

	if !m.currentFooterShowsTodayHint() {
		t.Fatal("expected today hint when selected day is not today")
	}

	m.day.cursor = today
	if m.currentFooterShowsTodayHint() {
		t.Fatal("expected today hint to hide when selected day is today")
	}
}

func TestModel_CurrentFooterContext_AgendaTracksSelectedRowKind(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	eventDay := time.Date(2026, 4, 24, 9, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        7,
		Title:     "Demo",
		StartTime: eventDay,
		EndTime:   eventDay.Add(time.Hour),
	}

	agendaWithEvent := NewAgendaModel(today).SetEvents([]event.Event{ev}, nil)
	m := Model{viewMode: viewAgenda, focus: focusCalendar, agenda: agendaWithEvent}
	if got := m.currentFooterContext(); got != FooterAgenda {
		t.Fatalf("currentFooterContext() = %v, want %v", got, FooterAgenda)
	}

	agendaEmpty := NewAgendaModel(today).SetShowEmptyDays(true).SetEvents([]event.Event{ev}, nil)
	m.agenda = agendaEmpty
	if got := m.currentFooterContext(); got != FooterAgendaEmpty {
		t.Fatalf("currentFooterContext() = %v, want %v", got, FooterAgendaEmpty)
	}
}
