package tui

import (
	"slices"
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
)

func makeCalListDialogFixture() CalendarListDialogModel {
	cals := map[int64]CalendarInfo{
		1: {Name: "Alpha", DisplayOrder: 0},
		2: {Name: "Bravo", DisplayOrder: 1},
		3: {Name: "Charlie", DisplayOrder: 2},
	}
	return NewCalendarListDialogModel(cals, nil, help.New())
}

// TestCalendarListDialog_MoveSelectedReordersAndEmits verifies the manage
// dialog reorders the selected calendar, keeps the selection on it, and emits
// the full new order for the app to persist.
func TestCalendarListDialog_MoveSelectedReordersAndEmits(t *testing.T) {
	m := makeCalListDialogFixture()
	m.shell = m.shell.SetSelected(0) // Alpha (id 1)

	m2, cmd := m.moveSelected(1) // move Alpha down
	if cmd == nil {
		t.Fatal("expected a reorder command")
	}
	msg, ok := cmd().(CalendarReorderedMsg)
	if !ok {
		t.Fatalf("expected CalendarReorderedMsg, got %T", cmd())
	}
	if want := []int64{2, 1, 3}; !slices.Equal(msg.IDs, want) {
		t.Fatalf("reordered IDs = %v, want %v", msg.IDs, want)
	}
	if id, ok := m2.selectedID(); !ok || id != 1 {
		t.Errorf("selection should follow the moved calendar: got %d (ok=%v), want 1", id, ok)
	}
}

// TestCalendarListDialog_MoveSelectedEdgesAreNoops verifies moving past either
// end of the list does nothing and emits no command.
func TestCalendarListDialog_MoveSelectedEdgesAreNoops(t *testing.T) {
	m := makeCalListDialogFixture()

	m.shell = m.shell.SetSelected(0)
	if _, cmd := m.moveSelected(-1); cmd != nil {
		t.Error("moveSelected(-1) at top should be a no-op")
	}
	m.shell = m.shell.SetSelected(2)
	if _, cmd := m.moveSelected(1); cmd != nil {
		t.Error("moveSelected(+1) at bottom should be a no-op")
	}
}

// TestCalendarListDialog_MoveSelectedDoesNotMutateOriginalOrder guards against
// the value receiver aliasing the parent's order slice via an in-place swap.
func TestCalendarListDialog_MoveSelectedDoesNotMutateOriginalOrder(t *testing.T) {
	m := makeCalListDialogFixture()
	m.shell = m.shell.SetSelected(0)
	_, _ = m.moveSelected(1)
	if want := []int64{1, 2, 3}; !slices.Equal(m.order, want) {
		t.Errorf("original order mutated by moveSelected: got %v, want %v", m.order, want)
	}
}

// TestCalendarListDialog_ReorderIgnoredWhenActionsFocused locks in the
// focus-zone guard: a move key pressed while the action buttons (not the list)
// own focus must not reorder or persist anything.
func TestCalendarListDialog_ReorderIgnoredWhenActionsFocused(t *testing.T) {
	m := makeCalListDialogFixture()
	m.shell = m.shell.SetSelected(0)
	m.shell = m.shell.SetFocusZone(ListZoneActions)

	m2, cmd := m.handleKey(tea.KeyPressMsg{Code: 'J', Text: "J"}) // move-down key
	if !slices.Equal(m2.order, []int64{1, 2, 3}) {
		t.Errorf("order changed while actions zone focused: got %v", m2.order)
	}
	if cmd != nil {
		if _, ok := cmd().(CalendarReorderedMsg); ok {
			t.Error("reorder emitted while actions zone focused")
		}
	}
}

// TestCalendarListDialog_MoveSelectedSingleAndEmpty covers the bounds-guard
// edge cases the 3-item fixture can't reach.
func TestCalendarListDialog_MoveSelectedSingleAndEmpty(t *testing.T) {
	single := NewCalendarListDialogModel(map[int64]CalendarInfo{1: {Name: "Solo"}}, nil, help.New())
	single.shell = single.shell.SetSelected(0)
	if _, cmd := single.moveSelected(1); cmd != nil {
		t.Error("single-item move down should be a no-op")
	}
	if _, cmd := single.moveSelected(-1); cmd != nil {
		t.Error("single-item move up should be a no-op")
	}

	empty := NewCalendarListDialogModel(map[int64]CalendarInfo{}, nil, help.New())
	if _, cmd := empty.moveSelected(1); cmd != nil {
		t.Error("empty-list move should be a no-op")
	}
}
