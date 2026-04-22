package tui

import (
	"testing"
)

func makeListFixture() CalendarListModel {
	items := []CalendarListItem{
		{ID: 1, Name: "Default", Color: "#a6e3a1"},
		{ID: 2, Name: "Family", Color: "#f5c2e7"},
	}
	return NewCalendarListModel(items, map[int64]bool{2: true})
}

func TestCalendarList_SpaceTogglesVisibility(t *testing.T) {
	m := makeListFixture().Focus()
	m.cursor = 0 // Default
	nextM, cmd := m.toggleCurrent()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 1 || !msg.Hidden {
		t.Errorf("got %+v want {ID:1 Hidden:true}", msg)
	}
	if !nextM.hidden[1] {
		t.Errorf("local hidden state not updated")
	}
}

func TestCalendarList_MoveCursorAdvances(t *testing.T) {
	m := makeListFixture().Focus()
	m.cursor = 0
	m = m.moveCursor(1)
	if m.cursor != 1 {
		t.Errorf("moveCursor(+1): cursor got %d want 1", m.cursor)
	}
}

func TestCalendarList_MoveCursorClampsAtEdges(t *testing.T) {
	m := makeListFixture().Focus()
	m.cursor = 0
	m = m.moveCursor(-1)
	if m.cursor != 0 {
		t.Errorf("moveCursor(-1) at top: got %d want 0", m.cursor)
	}
	m.cursor = m.RowCount() - 1
	m = m.moveCursor(1)
	if m.cursor != m.RowCount()-1 {
		t.Errorf("moveCursor(+1) at bottom: got %d want %d", m.cursor, m.RowCount()-1)
	}
}

func TestCalendarList_HandleClickTogglesItem(t *testing.T) {
	m := makeListFixture().Focus()
	m.cursor = 0
	nextM, cmd := m.HandleClick(0, 1) // second item (Family, initially hidden)
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 2 || msg.Hidden {
		t.Errorf("got %+v want {ID:2 Hidden:false}", msg)
	}
	if nextM.cursor != 1 {
		t.Errorf("cursor not moved to clicked row: got %d want 1", nextM.cursor)
	}
}

func TestCalendarList_HandleClickBelowListIsNoop(t *testing.T) {
	m := makeListFixture().Focus()
	m.cursor = 0
	nextM, cmd := m.HandleClick(0, 5) // past the last item
	if cmd != nil {
		t.Errorf("expected no command for click past end of list")
	}
	if nextM.cursor != 0 {
		t.Errorf("cursor should not move on click past end of list")
	}
}

func TestCalendarList_SetItemsPrunesStaleHiddenIDs(t *testing.T) {
	m := makeListFixture() // hidden = {2: true}
	m = m.SetItems([]CalendarListItem{{ID: 1, Name: "Default", Color: "#a6e3a1"}})
	if m.hidden[2] {
		t.Errorf("stale ID 2 should have been pruned: %v", m.hidden)
	}
}
