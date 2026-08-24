package tui

import (
	"strings"
	"testing"
)

func TestCalendarDeleteCountIgnoredAfterPendingCleared(t *testing.T) {
	m := NewModel(nil, "")
	m.calendarManagerOpen = true
	next, _ := m.handleCalendarDeleteCount(calendarDeleteCountMsg{
		id: 1, name: "Work", eventCount: 3,
	})
	got := next.(Model)
	if got.confirmOpen || got.pending.kind == pendingActionCalendarDelete {
		t.Fatal("unarmed delete count armed a confirm")
	}
}

func TestCalendarDeleteCountArmsWhenPendingMatches(t *testing.T) {
	m := NewModel(nil, "")
	m.calendarManagerOpen = true
	m = m.armCalendarDeleteCount(1, 0, "Work")
	next, _ := m.handleCalendarDeleteCount(calendarDeleteCountMsg{
		id: 1, name: "Work", eventCount: 3,
	})
	got := next.(Model)
	if !got.confirmOpen || got.pending.kind != pendingActionCalendarDelete {
		t.Fatalf("matching count did not arm confirm: open=%v kind=%v", got.confirmOpen, got.pending.kind)
	}
	if got.pending.target.calendarID != 1 {
		t.Fatalf("confirm target = %d, want 1", got.pending.target.calendarID)
	}
}

func TestCalendarDeleteCountIgnoredWhenQuitArmed(t *testing.T) {
	m := NewModel(nil, "")
	m.calendarManagerOpen = true
	m = m.armCalendarDeleteCount(1, 0, "Work")
	m = m.clearPending()
	m = m.openQuitConfirm()
	next, _ := m.handleCalendarDeleteCount(calendarDeleteCountMsg{
		id: 1, name: "Work", eventCount: 3,
	})
	got := next.(Model)
	if got.pending.kind != pendingActionQuit {
		t.Fatalf("stale count replaced quit: kind=%v", got.pending.kind)
	}
	plain := stripANSI(got.confirmDialog.View())
	if strings.Contains(plain, "Delete calendar") {
		t.Fatalf("stale count opened delete confirm over quit:\n%s", plain)
	}
}
