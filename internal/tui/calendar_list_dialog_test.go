package tui

import (
	"slices"
	"strings"
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

func TestCalendarListDialogGroupsRowsByAccount(t *testing.T) {
	calendars := map[int64]CalendarInfo{
		1: {Name: "On device", DisplayOrder: 9},
		2: {Name: "Company", AccountID: 9, AccountName: "Work", AccountOrder: 0, DisplayOrder: 0},
		3: {Name: "Primary", AccountID: 7, AccountName: "Google", AccountOrder: 1, DisplayOrder: 0},
		4: {Name: "Holidays", AccountID: 7, AccountName: "Google", AccountOrder: 1, DisplayOrder: 1},
	}
	m := NewCalendarListDialogModel(calendars, nil, help.New()).SetSize(120, 40)

	local := calendarManagerRowIndex(m, "Local")
	work := calendarManagerRowIndex(m, "Work")
	google := calendarManagerRowIndex(m, "Google")
	if local < 0 || work < 0 || google < 0 {
		t.Fatalf("account headings missing from rows: %q", plainCalendarManagerRows(m))
	}
	if local >= work || work >= google {
		t.Fatalf("account heading order = Local %d, Work %d, Google %d", local, work, google)
	}
	for _, heading := range []int{local, work, google} {
		if !m.shell.rowDisabled[heading] {
			t.Fatalf("account heading row %d is selectable", heading)
		}
	}
}

func calendarManagerRowIndex(m CalendarListDialogModel, label string) int {
	for i, row := range m.shell.rows {
		if strings.TrimSpace(stripANSI(row)) == label {
			return i
		}
	}
	return -1
}

func plainCalendarManagerRows(m CalendarListDialogModel) []string {
	rows := make([]string, len(m.shell.rows))
	for i, row := range m.shell.rows {
		rows[i] = strings.TrimSpace(stripANSI(row))
	}
	return rows
}

func TestCalendarListDialogReordersOnlyWithinAccount(t *testing.T) {
	calendars := map[int64]CalendarInfo{
		1: {Name: "Local", DisplayOrder: 0},
		2: {Name: "Primary", AccountID: 7, AccountName: "Google", AccountOrder: 0, DisplayOrder: 0},
		3: {Name: "Holidays", AccountID: 7, AccountName: "Google", AccountOrder: 0, DisplayOrder: 1},
	}
	m := NewCalendarListDialogModel(calendars, nil, help.New())
	m = m.selectCalendar(2).refresh()
	if _, cmd := m.moveSelected(-1); cmd != nil {
		t.Fatal("first calendar in an account moved across its account heading")
	}

	m = m.selectCalendar(3).refresh()
	m, cmd := m.moveSelected(-1)
	if cmd == nil {
		t.Fatal("calendar did not move within its account")
	}
	msg := cmd().(CalendarReorderedMsg)
	if want := []int64{1, 3, 2}; !slices.Equal(msg.IDs, want) {
		t.Fatalf("reordered IDs = %v, want %v", msg.IDs, want)
	}
	if id, ok := m.selectedID(); !ok || id != 3 {
		t.Fatalf("selection after reorder = %d, %v; want calendar 3", id, ok)
	}
}

func TestCalendarListDialogPreservesCalendarSelectionAcrossRefresh(t *testing.T) {
	calendars := map[int64]CalendarInfo{
		1: {Name: "Local"},
		2: {Name: "Primary", AccountID: 7, AccountName: "Google", DisplayOrder: 0},
		3: {Name: "Holidays", AccountID: 7, AccountName: "Google", DisplayOrder: 1},
	}
	m := NewCalendarListDialogModel(calendars, nil, help.New()).selectCalendar(3).refresh()
	calendars[4] = CalendarInfo{Name: "Another local", DisplayOrder: -1}
	m = m.SetCalendars(calendars, nil)
	if id, ok := m.selectedID(); !ok || id != 3 {
		t.Fatalf("selection after refresh = %d, %v; want calendar 3", id, ok)
	}
}

func TestCalendarListDialogActionsTargetSelectedCalendar(t *testing.T) {
	calendars := map[int64]CalendarInfo{
		1: {Name: "Local"},
		2: {Name: "Primary", AccountID: 7, AccountName: "Google"},
	}
	m := NewCalendarListDialogModel(calendars, nil, help.New()).selectCalendar(2).refresh()
	actions := m.buildActions()
	if len(actions) == 0 {
		t.Fatal("selected remote calendar has no actions")
	}
	edit, ok := actions[0].Msg().(CalendarDialogRequestedMsg)
	if !ok || edit.ID != 2 {
		t.Fatalf("edit action = %#v, want calendar 2", actions[0].Msg())
	}
}

func TestCalendarListDialogDetailsNameOwningAccount(t *testing.T) {
	calendars := map[int64]CalendarInfo{
		1: {Name: "Primary", AccountID: 7, AccountName: "Google", Synced: true},
	}
	m := NewCalendarListDialogModel(calendars, nil, help.New()).SetSize(120, 40)
	details := stripANSI(strings.Join(m.shell.detailLines, "\n"))
	if !strings.Contains(details, "Account") || !strings.Contains(details, "Google") {
		t.Fatalf("calendar details omit owning account:\n%s", details)
	}
}

func TestCalendarListDialogLabelsLocalCreation(t *testing.T) {
	m := makeCalListDialogFixture()
	if m.shell.titleAction == nil || m.shell.titleAction.Label != "+ New Local Calendar" {
		t.Fatalf("calendar creation action = %#v, want New Local Calendar", m.shell.titleAction)
	}
}

// TestCalendarListDialog_MoveSelectedReordersAndEmits verifies the manage
// dialog reorders the selected calendar, keeps the selection on it, and emits
// the full new order for the app to persist.
func TestCalendarListDialog_MoveSelectedReordersAndEmits(t *testing.T) {
	m := makeCalListDialogFixture()
	m = m.selectCalendar(1).refresh() // Alpha

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

	m = m.selectCalendar(1).refresh()
	if _, cmd := m.moveSelected(-1); cmd != nil {
		t.Error("moveSelected(-1) at top should be a no-op")
	}
	m = m.selectCalendar(3).refresh()
	if _, cmd := m.moveSelected(1); cmd != nil {
		t.Error("moveSelected(+1) at bottom should be a no-op")
	}
}

// TestCalendarListDialog_MoveSelectedDoesNotMutateOriginalOrder guards against
// the value receiver aliasing the parent's order slice via an in-place swap.
func TestCalendarListDialog_MoveSelectedDoesNotMutateOriginalOrder(t *testing.T) {
	m := makeCalListDialogFixture()
	m = m.selectCalendar(1).refresh()
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
	m = m.selectCalendar(1).refresh()
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
