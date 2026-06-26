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

func TestAgendaSelectCurrentOrNext_PicksOngoingEvent(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	now := time.Date(2026, 4, 23, 10, 30, 0, 0, time.Local)
	past := event.Event{
		ID: 1, Title: "Standup",
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 9, 30, 0, 0, time.Local),
	}
	ongoing := event.Event{
		ID: 2, Title: "Deep work",
		StartTime: time.Date(2026, 4, 23, 10, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 11, 0, 0, 0, time.Local),
	}
	later := event.Event{
		ID: 3, Title: "Review",
		StartTime: time.Date(2026, 4, 23, 14, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 15, 0, 0, 0, time.Local),
	}

	m := NewAgendaModel(day).
		SelectCurrentOrNext(now).
		SetEvents([]event.Event{past, ongoing, later}, nil)

	ev, ok := m.SelectedEvent()
	if !ok {
		t.Fatal("expected an event to be selected")
	}
	if ev.ID != ongoing.ID {
		t.Fatalf("selected event ID = %d, want %d (ongoing)", ev.ID, ongoing.ID)
	}
}

func TestAgendaSelectCurrentOrNext_PicksNextWhenNothingOngoing(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.Local)
	past := event.Event{
		ID: 1, Title: "Standup",
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 9, 30, 0, 0, time.Local),
	}
	upcoming := event.Event{
		ID: 2, Title: "Review",
		StartTime: time.Date(2026, 4, 23, 14, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 15, 0, 0, 0, time.Local),
	}

	m := NewAgendaModel(day).
		SelectCurrentOrNext(now).
		SetEvents([]event.Event{past, upcoming}, nil)

	ev, ok := m.SelectedEvent()
	if !ok {
		t.Fatal("expected an event to be selected")
	}
	if ev.ID != upcoming.ID {
		t.Fatalf("selected event ID = %d, want %d (upcoming)", ev.ID, upcoming.ID)
	}
}

func TestAgendaSelectCurrentOrNext_FallsBackWhenAllPast(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	now := time.Date(2026, 4, 23, 23, 0, 0, 0, time.Local)
	early := event.Event{
		ID: 1, Title: "Standup",
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 9, 30, 0, 0, time.Local),
	}
	late := event.Event{
		ID: 2, Title: "Review",
		StartTime: time.Date(2026, 4, 23, 14, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 15, 0, 0, 0, time.Local),
	}

	m := NewAgendaModel(day).
		SelectCurrentOrNext(now).
		SetEvents([]event.Event{early, late}, nil)

	ev, ok := m.SelectedEvent()
	if !ok {
		t.Fatal("expected an event to be selected")
	}
	// With no current/upcoming event, the regular fallback wins: the first
	// event of the cursor day.
	if ev.ID != early.ID {
		t.Fatalf("selected event ID = %d, want %d (first of day)", ev.ID, early.ID)
	}
}

func TestAgendaSelectCurrentOrNext_SurvivesEmptyFirstLoad(t *testing.T) {
	// At app startup, the host may call SetEvents with an empty event slice
	// (e.g., calendarsLoadedMsg arriving before eventsLoadedMsg drives
	// refreshCalendarViews with m.events still nil). The pending flag must
	// survive that empty load so the real event load can still consume it.
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	now := time.Date(2026, 4, 23, 10, 30, 0, 0, time.Local)
	ongoing := event.Event{
		ID: 2, Title: "Deep work",
		StartTime: time.Date(2026, 4, 23, 10, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 11, 0, 0, 0, time.Local),
	}

	m := NewAgendaModel(day).
		SelectCurrentOrNext(now).
		SetEvents(nil, nil) // empty first load — flag must persist
	m = m.SetEvents([]event.Event{ongoing}, nil)

	ev, ok := m.SelectedEvent()
	if !ok {
		t.Fatal("expected an event to be selected")
	}
	if ev.ID != ongoing.ID {
		t.Fatalf("selected event ID = %d, want %d (ongoing)", ev.ID, ongoing.ID)
	}
}

// TestAgendaHandleClick_EmptyStateButtonBounds verifies that the click hit-test
// for the "+ Create event" pill in the empty state matches the rendered pill
// bounds exactly. The pill is rendered at x=0 with width btnW, so:
//   - x=0        (left edge)        must trigger EventCreateMsg
//   - x=btnW-1   (right edge)       must trigger EventCreateMsg
//   - x=btnW     (one past right)   must be a noop
func TestAgendaHandleClick_EmptyStateButtonBounds(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	// No events → empty state. Height 10 ensures y=4 is inside the viewport.
	m := NewAgendaModel(day).SetEvents(nil, nil).SetSize(80, 10)

	btnW, btnY := m.emptyButtonBounds()
	if btnW <= 0 {
		t.Fatalf("emptyButtonBounds: width = %d, want > 0", btnW)
	}

	// x=0: left edge of the pill — must fire EventCreateMsg.
	_, cmd := m.HandleClick(0, btnY)
	if cmd == nil {
		t.Fatal("HandleClick(x=0, y=btnY): got nil cmd, want EventCreateMsg (left pill edge)")
	}
	if _, ok := cmd().(EventCreateMsg); !ok {
		t.Fatalf("HandleClick(x=0, y=btnY): cmd() = %T, want EventCreateMsg", cmd())
	}

	// x=btnW-1: right edge of the pill — must fire EventCreateMsg.
	_, cmd = m.HandleClick(btnW-1, btnY)
	if cmd == nil {
		t.Fatal("HandleClick(x=btnW-1, y=btnY): got nil cmd, want EventCreateMsg (right pill edge)")
	}
	if _, ok := cmd().(EventCreateMsg); !ok {
		t.Fatalf("HandleClick(x=btnW-1, y=btnY): cmd() = %T, want EventCreateMsg", cmd())
	}

	// x=btnW: one column past the right edge — must be a noop.
	_, cmd = m.HandleClick(btnW, btnY)
	if cmd != nil {
		t.Fatalf("HandleClick(x=btnW, y=btnY): got %T, want nil (outside pill)", cmd())
	}
}

func TestAgendaSelectCurrentOrNext_OneShotClearsAfterSetEvents(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	now := time.Date(2026, 4, 23, 10, 30, 0, 0, time.Local)
	first := event.Event{
		ID: 1, Title: "Standup",
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 9, 30, 0, 0, time.Local),
	}
	second := event.Event{
		ID: 2, Title: "Review",
		StartTime: time.Date(2026, 4, 23, 14, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 15, 0, 0, 0, time.Local),
	}

	// First load consumes the pending flag and lands on the upcoming event.
	m := NewAgendaModel(day).
		SelectCurrentOrNext(now).
		SetEvents([]event.Event{first, second}, nil)
	if ev, _ := m.SelectedEvent(); ev.ID != second.ID {
		t.Fatalf("first load: selected ID = %d, want %d", ev.ID, second.ID)
	}

	// A subsequent SetEvents must preserve the user's selection (no
	// recurring "snap to upcoming" — the flag is one-shot).
	m = m.SetEvents([]event.Event{first, second}, nil)
	if ev, _ := m.SelectedEvent(); ev.ID != second.ID {
		t.Fatalf("second load: selected ID = %d, want %d (selection preserved)", ev.ID, second.ID)
	}
}
