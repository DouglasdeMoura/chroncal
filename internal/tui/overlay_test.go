package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWheelOverConfirmDoesNotScrollWeek(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.viewMode = viewWeek
	m.week.scrollOffset = 4
	m.week.linesPerHour = 2
	m = m.openQuitConfirm()

	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	got := next.(Model)
	if got.week.scrollOffset != 4 {
		t.Fatalf("wheel over quit confirm scrolled the week view: offset=%d", got.week.scrollOffset)
	}
	if !got.confirmOpen || got.pending.kind != pendingActionQuit {
		t.Fatalf("quit confirm lost: open=%v kind=%v", got.confirmOpen, got.pending.kind)
	}
}

func TestWheelOverKeepLocalConfirmDoesNotScrollWeek(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.viewMode = viewWeek
	m.week.scrollOffset = 4
	m.week.linesPerHour = 2
	updated, _ := m.Update(CalendarKeepLocalRequestedMsg{ID: 1, Name: "Personal"})
	m = updated.(Model)
	if !m.confirmOpen {
		t.Fatal("keep-local confirm did not open")
	}

	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	got := next.(Model)
	if got.week.scrollOffset != 4 {
		t.Fatalf("wheel over keep-local confirm scrolled the week view: offset=%d", got.week.scrollOffset)
	}
}

func TestOverlayStack_QuitConfirmIsTop(t *testing.T) {
	m := NewModel(nil, "")
	m = m.openQuitConfirm()
	layers := m.overlayStack()
	if len(layers) == 0 || layers[0].kind != overlayQuitConfirm {
		t.Fatalf("quit confirm should be the top overlay, got %#v", kinds(layers))
	}
}

func kinds(layers []overlayLayer) []overlayKind {
	out := make([]overlayKind, len(layers))
	for i, l := range layers {
		out[i] = l.kind
	}
	return out
}
