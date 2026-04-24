package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestDefaultAgendaKeys_ReservesEForEdit(t *testing.T) {
	keys := defaultAgendaKeys()

	if got := keys.ToggleEmpty.Keys(); len(got) != 1 || got[0] != "o" {
		t.Fatalf("ToggleEmpty keys = %v, want [o]", got)
	}

	help := keys.ToggleEmpty.Help()
	if help.Key != "o" {
		t.Fatalf("ToggleEmpty help key = %q, want %q", help.Key, "o")
	}
	if help.Desc != "empty days" {
		t.Fatalf("ToggleEmpty help desc = %q, want %q", help.Desc, "empty days")
	}
}

func TestAgendaUpdate_XKeyRequestsDeleteForSelectedEvent(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        42,
		Title:     "Planning",
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 10, 0, 0, 0, time.Local),
	}

	m := NewAgendaModel(day).SetEvents([]event.Event{ev}, nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd == nil {
		t.Fatal("expected a command for 'x'")
	}

	msg, ok := cmd().(EventDeleteMsg)
	if !ok {
		t.Fatalf("expected EventDeleteMsg, got %T", cmd())
	}
	if msg.Event.ID != ev.ID {
		t.Fatalf("Event.ID = %d, want %d", msg.Event.ID, ev.ID)
	}
}

func TestAgendaUpdate_XKeyNoopOnEmptyDayPlaceholder(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)

	m := NewAgendaModel(day).SetShowEmptyDays(true).SetEvents(nil, nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd != nil {
		t.Fatalf("expected no command for empty-day placeholder, got %T", cmd())
	}
}

func TestAgendaHandleClick_BelowViewportIsNoop(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        7,
		Title:     "Standup",
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 9, 30, 0, 0, time.Local),
	}

	m := NewAgendaModel(day).SetEvents([]event.Event{ev}, nil).SetSize(80, 5)

	// A click at y == m.height (or beyond) lands in the footer area, not on
	// any agenda row, and must not open an event.
	_, cmd := m.HandleClick(10, 5)
	if cmd != nil {
		t.Fatalf("click at y=m.height produced a command (%T); want noop", cmd())
	}
	_, cmd = m.HandleClick(10, 10)
	if cmd != nil {
		t.Fatalf("click well below viewport produced a command (%T); want noop", cmd())
	}
}
