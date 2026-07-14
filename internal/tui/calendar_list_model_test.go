package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
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

func TestCalendarList_MoveCurrentSwapsAndEmitsOrder(t *testing.T) {
	m := makeListFixture().Focus()
	m.cursor = 0 // Default (ID 1)
	nextM, cmd := m.moveCurrent(1)
	if cmd == nil {
		t.Fatal("expected a reorder command")
	}
	msg, ok := cmd().(CalendarReorderedMsg)
	if !ok {
		t.Fatalf("expected CalendarReorderedMsg, got %T", cmd())
	}
	if want := []int64{2, 1}; len(msg.IDs) != 2 || msg.IDs[0] != want[0] || msg.IDs[1] != want[1] {
		t.Errorf("reordered IDs got %v want %v", msg.IDs, want)
	}
	if nextM.cursor != 1 {
		t.Errorf("cursor should follow the moved item: got %d want 1", nextM.cursor)
	}
	if nextM.items[1].ID != 1 {
		t.Errorf("moved item not at new position: %+v", nextM.items)
	}
}

func TestCalendarList_MoveCurrentAtEdgesIsNoop(t *testing.T) {
	m := makeListFixture().Focus()
	m.cursor = 0
	if _, cmd := m.moveCurrent(-1); cmd != nil {
		t.Error("moveCurrent(-1) at top should be a no-op")
	}
	m.cursor = m.RowCount() - 1
	if _, cmd := m.moveCurrent(1); cmd != nil {
		t.Error("moveCurrent(+1) at bottom should be a no-op")
	}
}

func TestCalendarList_MoveCurrentDoesNotMutateOriginal(t *testing.T) {
	m := makeListFixture().Focus()
	m.cursor = 0
	_, _ = m.moveCurrent(1)
	// The receiver's backing array must be untouched: the parent still holds
	// this slice until it commits the returned model.
	if m.items[0].ID != 1 || m.items[1].ID != 2 {
		t.Errorf("original items mutated by moveCurrent: %+v", m.items)
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

func TestCalendarList_ViewTruncatesLongNamesWithEllipsis(t *testing.T) {
	items := []CalendarListItem{
		{ID: 1, Name: "GMX", Color: "#a6e3a1"},
		{ID: 2, Name: "maildodouglas@gmail.com", Color: "#f5c2e7"},
	}
	m := NewCalendarListModel(items, nil).SetWidth(22)
	out := m.View()
	if !strings.Contains(out, "GMX") {
		t.Errorf("short name should render untruncated; got %q", out)
	}
	if strings.Contains(out, "maildodouglas@gmail.com") {
		t.Errorf("long name should be truncated; got %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis in output; got %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 22 {
			t.Errorf("rendered line exceeds width 22 (got %d): %q", w, line)
		}
	}
}

func TestCalendarList_ViewWithoutWidthDoesNotTruncate(t *testing.T) {
	items := []CalendarListItem{
		{ID: 1, Name: "maildodouglas@gmail.com", Color: "#f5c2e7"},
	}
	m := NewCalendarListModel(items, nil) // no SetWidth call
	out := m.View()
	if !strings.Contains(out, "maildodouglas@gmail.com") {
		t.Errorf("expected full name when width is unset; got %q", out)
	}
	if strings.Contains(out, "…") {
		t.Errorf("did not expect ellipsis when width is unset; got %q", out)
	}
}

func groupedListFixture() CalendarListModel {
	items := []CalendarListItem{
		{ID: 1, Name: "On device", Color: "#a6e3a1", AccountName: "Local"},
		{ID: 2, Name: "Personal", Color: "#f5c2e7", AccountID: 7, AccountName: "Google"},
		{ID: 3, Name: "Holidays in Brazil", Color: "#89b4fa", AccountID: 7, AccountName: "Google", Access: "read"},
		{ID: 4, Name: "Team", Color: "#fab387", AccountID: 9, AccountName: "Work", Missing: true},
	}
	return NewCalendarListModel(items, nil).SetSize(50, 20).Focus()
}

func TestCalendarList_GroupsCalendarsByAccount(t *testing.T) {
	m := groupedListFixture()
	out := m.View()
	for _, want := range []string{"Local", "Google", "Work", "Holidays in Brazil", "[read-only]", "[missing]"} {
		if !strings.Contains(out, want) {
			t.Errorf("grouped view missing %q:\n%s", want, out)
		}
	}
	if m.RowCount() != 7 {
		t.Fatalf("row count = %d, want three headers plus four calendars", m.RowCount())
	}
}

func TestCalendarList_AccountHeaderCollapsesChildren(t *testing.T) {
	m := groupedListFixture()
	m.cursor = 2 // Google header
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("collapsing a group should not emit a parent command")
	}
	if !next.collapsed[7] {
		t.Fatal("Google group should be collapsed")
	}
	if next.RowCount() != 5 {
		t.Fatalf("collapsed row count = %d, want two Google children hidden", next.RowCount())
	}
	out := next.View()
	if strings.Contains(out, "Holidays in Brazil") || !strings.Contains(out, "Google") {
		t.Fatalf("collapsed view should keep header and hide children:\n%s", out)
	}
}

func TestCalendarList_AccountHeaderTogglesEveryChild(t *testing.T) {
	m := groupedListFixture()
	m.cursor = 2 // Google header
	next, cmd := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("expected batch visibility command")
	}
	msg, ok := cmd().(CalendarVisibilityBatchToggledMsg)
	if !ok {
		t.Fatalf("message = %T, want CalendarVisibilityBatchToggledMsg", cmd())
	}
	if len(msg.IDs) != 2 || msg.IDs[0] != 2 || msg.IDs[1] != 3 || !msg.Hidden {
		t.Fatalf("batch message = %+v", msg)
	}
	if !next.hidden[2] || !next.hidden[3] {
		t.Fatalf("group children were not hidden: %v", next.hidden)
	}
}

func TestCalendarList_ReorderStaysWithinAccount(t *testing.T) {
	m := groupedListFixture()
	m.cursor = 3 // Personal
	if _, cmd := m.moveCurrent(-1); cmd != nil {
		t.Fatal("calendar must not move across its account header")
	}
	next, cmd := m.moveCurrent(1)
	if cmd == nil {
		t.Fatal("calendar should move within its account")
	}
	msg := cmd().(CalendarReorderedMsg)
	if got, want := msg.IDs, []int64{1, 3, 2, 4}; len(got) != len(want) ||
		got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("reordered IDs = %v, want %v", got, want)
	}
	if next.cursor != 4 {
		t.Fatalf("cursor = %d, want moved calendar row 4", next.cursor)
	}
}

func TestCalendarList_ViewportKeepsCursorVisible(t *testing.T) {
	m := groupedListFixture().SetSize(32, 3)
	for m.cursor < m.RowCount()-1 {
		m = m.moveCursor(1)
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered lines = %d, want viewport height 3:\n%s", len(lines), m.View())
	}
	if !strings.Contains(m.View(), "Team") {
		t.Fatalf("last cursor row must be visible after scrolling:\n%s", m.View())
	}
	if m.offset == 0 {
		t.Fatal("viewport offset should advance for the final row")
	}
}
