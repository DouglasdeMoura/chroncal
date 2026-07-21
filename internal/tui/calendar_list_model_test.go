package tui

import (
	"maps"
	"slices"
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
func calendarListRowForCalendarID(t *testing.T, m CalendarListModel, id int64) int {
	t.Helper()
	for rowIndex, row := range m.rows {
		if row.kind == calendarRow && m.items[row.itemIndex].ID == id {
			return rowIndex
		}
	}
	t.Fatalf("calendar %d has no rendered row", id)
	return -1
}

func calendarListRowForAccountID(t *testing.T, m CalendarListModel, id int64) int {
	t.Helper()
	for rowIndex, row := range m.rows {
		if row.kind == accountHeaderRow && row.accountID == id {
			return rowIndex
		}
	}
	t.Fatalf("account %d has no rendered row", id)
	return -1
}

func TestCalendarList_GroupsCalendarsIntoCleanAccountSections(t *testing.T) {
	m := groupedListFixture()
	out := stripANSI(m.View())
	for _, want := range []string{"Local", "Google", "Work", "Holidays in Brazil"} {
		if !strings.Contains(out, want) {
			t.Errorf("grouped view missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"[read-only]", "[missing]", "Local 1", "Google 2", "Work 1"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("grouped view contains sidebar metadata %q:\n%s", unwanted, out)
		}
	}
	lines := strings.Split(out, "\n")
	blankAfter := func(name string) bool {
		for i, line := range lines[:len(lines)-1] {
			if strings.Contains(line, name) && strings.TrimSpace(lines[i+1]) == "" {
				return true
			}
		}
		return false
	}
	if !blankAfter("On device") || !blankAfter("Holidays in Brazil") {
		t.Fatalf("account sections should be separated by a blank row:\n%q", out)
	}
	if m.RowCount() != 9 {
		t.Fatalf("row count = %d, want three headers, four calendars, and two separators", m.RowCount())
	}
}

func TestCalendarList_GroupedRowsUseCompactLeadingSpacing(t *testing.T) {
	lines := strings.Split(stripANSI(groupedListFixture().View()), "\n")
	var header, calendar string
	for _, line := range lines {
		switch {
		case strings.Contains(line, "Local"):
			header = line
		case strings.Contains(line, "On device"):
			calendar = line
		}
	}
	if !strings.HasPrefix(header, "▾ Local") {
		t.Fatalf("account heading has leading space: %q", header)
	}
	if !strings.HasPrefix(calendar, " ● On device") {
		t.Fatalf("calendar row should have one space before and after its marker: %q", calendar)
	}
}

func TestCalendarList_NarrowGroupedSidebarRemainsScannable(t *testing.T) {
	m := groupedListFixture().SetSize(22, 20)
	out := stripANSI(m.View())
	for _, line := range strings.Split(out, "\n") {
		if lipgloss.Width(line) > 22 {
			t.Fatalf("narrow sidebar line width = %d, want <= 22: %q\n%s",
				lipgloss.Width(line), line, out)
		}
	}
	for _, want := range []string{"Local", "Google", "Work", "Personal"} {
		if !strings.Contains(out, want) {
			t.Fatalf("narrow sidebar missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"read-only", "missing", "Google 2"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("narrow sidebar contains noisy metadata %q:\n%s", unwanted, out)
		}
	}
}

func TestCalendarList_SelectedCalendarUsesThemeAccentInsteadOfReverseVideo(t *testing.T) {
	m := groupedListFixture().SetTheme(
		lipgloss.Color("#112233"),
		lipgloss.Color("#445566"),
		lipgloss.Color("#ddeeff"),
		lipgloss.Color("#ffffff"),
		lipgloss.Color("#ff0000"),
	)
	m.cursor = calendarListRowForCalendarID(t, m, 2)
	out := m.View()
	if strings.Contains(out, "\x1b[1;7m") {
		t.Fatalf("selected calendar uses reverse video instead of the theme accent: %q", out)
	}
	if !strings.Contains(out, "48;2;17;34;51") {
		t.Fatalf("selected calendar does not use accent background #112233: %q", out)
	}
	selectedName := lipgloss.NewStyle().
		Background(lipgloss.Color("#112233")).
		Foreground(lipgloss.Color("#ffffff")).
		Bold(true).
		Render(" Personal ")
	if !strings.Contains(out, selectedName) {
		t.Fatalf("selected calendar name does not carry the accent background: want %q in %q", selectedName, out)
	}
}

func TestCalendarList_AccountHeadersHaveNoCalendarMarker(t *testing.T) {
	for line := range strings.SplitSeq(stripANSI(groupedListFixture().View()), "\n") {
		if strings.Contains(line, "Google") && strings.ContainsAny(line, "●○◐") {
			t.Fatalf("account header must not render a calendar marker: %q", line)
		}
	}
}

func TestCalendarList_AccountHeaderUsesLeftAndRightForDisclosure(t *testing.T) {
	m := groupedListFixture()
	m.cursor = calendarListRowForAccountID(t, m, 7)

	collapsed, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Fatal("collapsing a group should not emit a parent command")
	}
	if !collapsed.collapsed[7] {
		t.Fatal("Google group should be collapsed")
	}
	if collapsed.RowCount() != 7 {
		t.Fatalf("collapsed row count = %d, want two Google children hidden", collapsed.RowCount())
	}
	if out := collapsed.View(); strings.Contains(out, "Holidays in Brazil") || !strings.Contains(out, "Google") {
		t.Fatalf("collapsed view should keep header and hide children:\n%s", out)
	}

	expanded, cmd := collapsed.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd != nil {
		t.Fatal("expanding a group should not emit a parent command")
	}
	if expanded.collapsed[7] || expanded.RowCount() != 9 {
		t.Fatalf("right should restore the expanded account section: collapsed=%v rows=%d", expanded.collapsed[7], expanded.RowCount())
	}
}

func TestCalendarList_AccountHeaderSpaceTogglesDisclosureWithoutTogglingCalendars(t *testing.T) {
	m := groupedListFixture()
	m.cursor = calendarListRowForAccountID(t, m, 7)
	collapsed, cmd := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd != nil {
		t.Fatalf("account heading Space emitted hidden bulk action %T", cmd())
	}
	if !collapsed.collapsed[7] {
		t.Fatal("account heading Space did not collapse its calendar rows")
	}
	if collapsed.hidden[2] || collapsed.hidden[3] {
		t.Fatalf("account heading Space changed child visibility: %v", collapsed.hidden)
	}

	expanded, cmd := collapsed.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd != nil {
		t.Fatalf("second account heading Space emitted command %T", cmd())
	}
	if expanded.collapsed[7] {
		t.Fatal("second account heading Space did not expand its calendar rows")
	}
}

func TestCalendarList_AccountHeaderEnterRequestsSettings(t *testing.T) {
	m := groupedListFixture()
	cursor := calendarListRowForAccountID(t, m, 7)
	m.cursor = cursor
	collapsedBefore := maps.Clone(m.collapsed)
	hiddenBefore := maps.Clone(m.hidden)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("account heading Enter should request Account settings")
	}
	msg, ok := cmd().(CalendarManagerRequestedMsg)
	if !ok || msg.Target != CalendarManagerTargetAccount || msg.AccountID != 7 {
		t.Fatalf("Account settings request = %#v", cmd())
	}
	// Requesting account actions is side-effect free: cursor, collapse, and
	// visibility must all be untouched. Discovery and the Account menu open
	// later, in response to the message, not at emit time.
	if updated.cursor != cursor {
		t.Errorf("cursor moved: got %d want %d", updated.cursor, cursor)
	}
	if !maps.Equal(updated.collapsed, collapsedBefore) {
		t.Errorf("collapse state changed: got %v want %v", updated.collapsed, collapsedBefore)
	}
	if !maps.Equal(updated.hidden, hiddenBefore) {
		t.Errorf("visibility state changed: got %v want %v", updated.hidden, hiddenBefore)
	}
}

func TestCalendarList_AccountHeaderMouseTargetsSeparateDisclosureAndSettings(t *testing.T) {
	m := groupedListFixture()
	header := calendarListRowForAccountID(t, m, 7)

	collapsed, cmd := m.HandleClick(1, header)
	if cmd != nil || !collapsed.collapsed[7] {
		t.Fatalf("disclosure click: command=%v collapsed=%v", cmd, collapsed.collapsed[7])
	}

	collapsedBefore := maps.Clone(m.collapsed)
	hiddenBefore := maps.Clone(m.hidden)
	cursorBefore := m.cursor
	opened, cmd := m.HandleClick(6, header)
	if opened.collapsed[7] {
		t.Fatal("account name click should not collapse the section")
	}
	if cmd == nil {
		t.Fatal("account name click should request Account settings")
	}
	msg, ok := cmd().(CalendarManagerRequestedMsg)
	if !ok || msg.Target != CalendarManagerTargetAccount || msg.AccountID != 7 {
		t.Fatalf("Account settings request = %#v", cmd())
	}
	// The name-area click requests actions without disturbing cursor,
	// collapse, or visibility. The request is side-effect free; the menu
	// and discovery open later, in response to the message.
	if opened.cursor != cursorBefore {
		t.Errorf("cursor moved: got %d want %d", opened.cursor, cursorBefore)
	}
	if !maps.Equal(opened.collapsed, collapsedBefore) {
		t.Errorf("collapse state changed: got %v want %v", opened.collapsed, collapsedBefore)
	}
	if !maps.Equal(opened.hidden, hiddenBefore) {
		t.Errorf("visibility state changed: got %v want %v", opened.hidden, hiddenBefore)
	}
}

func TestCalendarList_CursorSkipsAccountSeparators(t *testing.T) {
	m := groupedListFixture()
	m.cursor = calendarListRowForCalendarID(t, m, 1)
	next := m.moveCursor(1)
	if next.rows[next.cursor].kind != accountHeaderRow || next.rows[next.cursor].accountID != 7 {
		t.Fatalf("cursor landed on noninteractive separator: row=%+v", next.rows[next.cursor])
	}
}

func TestCalendarList_ReorderStaysWithinAccount(t *testing.T) {
	m := groupedListFixture()
	m.cursor = calendarListRowForCalendarID(t, m, 2)
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
	if next.cursor != calendarListRowForCalendarID(t, next, 2) {
		t.Fatalf("cursor = %d, want moved calendar row %d", next.cursor, calendarListRowForCalendarID(t, next, 2))
	}
}

func TestCalendarList_ReordersRemoteAccountSectionsButKeepsLocalFirst(t *testing.T) {
	m := groupedListFixture()
	m.cursor = calendarListRowForAccountID(t, m, 7)
	if _, cmd := m.moveAccount(-1); cmd != nil {
		t.Fatal("remote account moved above the local section")
	}

	next, cmd := m.moveAccount(1)
	if cmd == nil {
		t.Fatal("remote account did not move below the adjacent remote account")
	}
	msg, ok := cmd().(AccountReorderedMsg)
	if !ok || !slices.Equal(msg.IDs, []int64{9, 7}) {
		t.Fatalf("account reorder message = %#v", cmd())
	}
	if next.items[0].AccountID != 0 || next.items[1].AccountID != 9 || next.items[2].AccountID != 7 {
		t.Fatalf("reordered account blocks = %+v", next.items)
	}
	if next.cursor != calendarListRowForAccountID(t, next, 7) {
		t.Fatalf("cursor did not follow moved account: %d", next.cursor)
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
