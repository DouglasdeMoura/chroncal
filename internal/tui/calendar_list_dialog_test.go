package tui

import (
	"slices"
	"testing"

	"charm.land/bubbles/v2/help"
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
