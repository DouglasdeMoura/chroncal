package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// flatManagerCalendars is a mixed Local + multi-account fixture whose
// canonical order (Local first, then accounts by AccountOrder, then
// in-account DisplayOrder) is [1, 2, 3, 4]:
//
//	1 On device  Local
//	2 Primary    Google   (account 7, order 0, display 0)
//	3 Holidays   Google   (account 7, order 0, display 1)
//	4 Work       Fastmail (account 9, order 1, display 0)
func flatManagerCalendars() map[int64]CalendarInfo {
	return map[int64]CalendarInfo{
		1: {Name: "On device", Color: "#ff0000", DisplayOrder: 9},
		2: {Name: "Primary", Color: "#00ff00", AccountID: 7, AccountName: "Google", AccountOrder: 0, DisplayOrder: 0},
		3: {Name: "Holidays", Color: "#0000ff", AccountID: 7, AccountName: "Google", AccountOrder: 0, DisplayOrder: 1},
		4: {Name: "Work", Color: "#aaaaaa", AccountID: 9, AccountName: "Fastmail", AccountOrder: 1, DisplayOrder: 0},
	}
}

func newFlatManager() CalendarManagerModel {
	return NewCalendarManagerModel(flatManagerCalendars(), nil, help.New()).SetSize(120, 40)
}

func managerCalendarLine(t *testing.T, m CalendarManagerModel, id int64) string {
	t.Helper()
	row := calendarListRowForCalendarID(t, m.list, id)
	start, end := m.list.viewportBounds()
	if row < start || row >= end {
		t.Fatalf("calendar %d row %d outside viewport [%d,%d)", id, row, start, end)
	}
	return strings.TrimSpace(stripANSI(strings.Split(m.list.View(), "\n")[row-start]))
}

// TestCalendarManagerRootEnterPushesCalendarDetail verifies Enter pushes the
// selected calendar's detail onto the manager stack (OpenCalendar), targeting
// the immutable ID and switching the screen without disturbing root selection.
func TestCalendarManagerRootEnterPushesCalendarDetail(t *testing.T) {
	m := newFlatManager().selectCalendar(3)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("Enter should push detail internally, got command %T", cmd())
	}
	if mm.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want CalendarManagerScreenCalendar", mm.Screen())
	}
	if mm.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	if got := mm.calendarForm.Draft().ID; got != 3 {
		t.Fatalf("pushed detail ID = %d, want 3", got)
	}
	// Root selection is preserved by ID so Back can restore it.
	if id, ok := mm.selectedID(); !ok || id != 3 {
		t.Fatalf("root selection moved on open: got %d ok=%v", id, ok)
	}
}

// TestCalendarManagerRootSpaceTogglesVisibility verifies Space targets the
// selected immutable ID and emits CalendarVisibilityToggledMsg.
func TestCalendarManagerRootSpaceTogglesVisibility(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	_, cmd := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("Space emitted no command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 2 {
		t.Fatalf("toggle msg ID = %d, want 2", msg.ID)
	}
}

// TestCalendarManagerRootMouseRowOpensEdit verifies clicking the non-checkbox
// row body follows the visible Edit affordance and opens the clicked calendar.
func TestCalendarManagerRootMouseRowOpensEdit(t *testing.T) {
	m := newFlatManager()
	listX, listY, _, _ := m.listRegion()
	row := calendarListRowForCalendarID(t, m.list, 3) - m.list.offset
	clicked, cmd := m.Update(tea.MouseClickMsg{X: listX + 8, Y: listY + row, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("row edit click emitted command %T", cmd())
	}
	if clicked.Screen() != CalendarManagerScreenCalendar || clicked.calendarForm == nil {
		t.Fatalf("row click did not open calendar edit: screen=%v form=%v", clicked.Screen(), clicked.calendarForm)
	}
	if got := clicked.calendarForm.Draft().ID; got != 3 {
		t.Fatalf("row click opened calendar %d, want 3", got)
	}
}

func TestCalendarManagerRootMouseCheckboxTogglesVisibility(t *testing.T) {
	m := newFlatManager()
	listX, listY, _, _ := m.listRegion()
	row := calendarListRowForCalendarID(t, m.list, 2) - m.list.offset
	toggled, cmd := m.Update(tea.MouseClickMsg{X: listX + 1, Y: listY + row, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("checkbox click emitted no visibility command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok || msg.ID != 2 || !msg.Hidden {
		t.Fatalf("checkbox click message = %#v, want calendar 2 hidden", cmd())
	}
	if toggled.Screen() != CalendarManagerScreenList || !toggled.hidden[2] {
		t.Fatalf("checkbox click opened edit or failed optimistic toggle: screen=%v hidden=%v", toggled.Screen(), toggled.hidden[2])
	}
}

// TestCalendarManagerAddActionLivesAtBottomOfSourceList verifies the header
// shows only "Calendars" and the compact + Add action sits one row below the
// source-list viewport at the source column's left edge.
func TestCalendarManagerAddActionLivesAtBottomOfSourceList(t *testing.T) {
	m := newFlatManager()
	for _, line := range strings.Split(stripANSI(m.View()), "\n") {
		if strings.Contains(line, "Calendars") && strings.Contains(line, "+ Add") {
			t.Fatalf("header still couples the Add action: %q", line)
		}
	}
	_, ay, aw, ok := m.sourceAddActionRect()
	if !ok {
		t.Fatal("source + Add action not present")
	}
	if aw != lipgloss.Width("+ Add") {
		t.Fatalf("source Add width = %d, want %d", aw, lipgloss.Width("+ Add"))
	}
	_, listY, _, listH := m.listRegion()
	if ay != listY+listH+1 {
		t.Fatalf("Add y = %d, want below list viewport %d", ay, listY+listH+1)
	}
}

// TestCalendarManagerAddActionMutedWhileDetailOwnsDraft verifies the + Add
// action remains visible (rendered) but is inactive while a pushed calendar
// detail owns an unsaved draft, so Add cannot silently discard it.
func TestCalendarManagerAddActionMutedWhileDetailOwnsDraft(t *testing.T) {
	m := newFlatManager()
	if !m.sourceAddActionActive() {
		t.Fatal("Add action should be active at the root")
	}
	m = m.OpenCalendar(calendarDialogParamsFor(1, m.calendars[1], false))
	if m.sourceAddActionActive() {
		t.Fatal("Add action should be inactive while a detail owns a draft")
	}
	if _, _, _, ok := m.sourceAddActionRect(); !ok {
		t.Fatal("muted Add action should remain rendered in wide mode")
	}
}

// TestCalendarManagerAddActionInactiveOnPushedScreens verifies the + Add
// action is muted and inert on every pushed screen — not just a calendar
// detail — because each pushed screen owns its own input. Covers the import
// transfer screen (which leaves no calendar draft) and the account screen.
func TestCalendarManagerAddActionInactiveOnPushedScreens(t *testing.T) {
	if m := newFlatManager(); !m.sourceAddActionActive() {
		t.Fatal("Add action should be active at the root")
	}
	for name, pushed := range map[string]CalendarManagerModel{
		"import":  newFlatManager().OpenImport(1),
		"account": newFlatManager().OpenAccount(AccountSettingsParams{AccountID: 7, DisplayName: "Google"}),
	} {
		if pushed.sourceAddActionActive() {
			t.Fatalf("%s screen: Add action should be inactive", name)
		}
		if _, _, _, ok := pushed.sourceAddActionRect(); !ok {
			t.Fatalf("%s screen: muted Add action should remain rendered in wide mode", name)
		}
	}
}

// TestCalendarManagerRootReorderWithinSameOwnerOnly verifies Shift+Up/Down
// emits CalendarReorderedMsg with the full canonical ID order, swaps only
// within the same AccountID, and is a no-op across an owner boundary.
func TestCalendarManagerRootReorderWithinSameOwnerOnly(t *testing.T) {
	// Canonical order: [1 Local, 2 Google, 3 Google, 4 Fastmail].

	// Move calendar 3 (Google) up over calendar 2 (Google): same owner, swaps.
	m := newFlatManager().selectCalendar(3)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'K', Text: "K"}) // shift+up alternate
	if cmd == nil {
		t.Fatal("within-owner move should emit a reorder command")
	}
	msg, ok := cmd().(CalendarReorderedMsg)
	if !ok {
		t.Fatalf("expected CalendarReorderedMsg, got %T", cmd())
	}
	if want := []int64{1, 3, 2, 4}; !slices.Equal(msg.IDs, want) {
		t.Fatalf("reordered IDs = %v, want %v", msg.IDs, want)
	}
	if id, ok := mm.selectedID(); !ok || id != 3 {
		t.Fatalf("selection should follow moved calendar: got %d ok=%v", id, ok)
	}

	// Move calendar 4 (Fastmail, idx 3) up over calendar 3 (Google, idx 2):
	// different owner -> no-op, no command.
	m2 := newFlatManager().selectCalendar(4)
	_, cmd = m2.Update(tea.KeyPressMsg{Code: 'K', Text: "K"})
	if cmd != nil {
		if _, ok := cmd().(CalendarReorderedMsg); ok {
			t.Fatal("cross-owner reorder must be a no-op (Fastmail over Google)")
		}
	}

	// Move calendar 2 (Google, idx 1) up over calendar 1 (Local, idx 0):
	// different owner -> no-op.
	m3 := newFlatManager().selectCalendar(2)
	_, cmd = m3.Update(tea.KeyPressMsg{Code: 'K', Text: "K"})
	if cmd != nil {
		if _, ok := cmd().(CalendarReorderedMsg); ok {
			t.Fatal("cross-owner reorder must be a no-op (Google over Local)")
		}
	}
}

// TestCalendarManagerRootReorderEdgesAreNoops verifies moving past either end of
// the list is a no-op that emits nothing.
func TestCalendarManagerRootReorderEdgesAreNoops(t *testing.T) {
	m := newFlatManager()
	// Top calendar (Local) cannot move up.
	m = m.selectCalendar(1)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'K', Text: "K"}); cmd != nil {
		t.Error("move up at top should be a no-op")
	}
	// Bottom calendar (Fastmail) cannot move down.
	m = m.selectCalendar(4)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'J', Text: "J"}); cmd != nil {
		t.Error("move down at bottom should be a no-op")
	}
}

// TestCalendarManagerRootReorderDoesNotMutateOriginalOrder guards against the
// value receiver aliasing the parent's order slice via an in-place swap.
func TestCalendarManagerRootReorderDoesNotMutateOriginalOrder(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	_, _ = m.Update(tea.KeyPressMsg{Code: 'J', Text: "J"})
	got := make([]int64, len(m.list.items))
	for i, item := range m.list.items {
		got[i] = item.ID
	}
	if want := []int64{1, 2, 3, 4}; !slices.Equal(got, want) {
		t.Errorf("original order mutated by reorder: got %v, want %v", got, want)
	}
}

// TestCalendarManagerRootSelectionRestoredByID verifies SetData preserves the
// selected calendar (by immutable ID) and the scroll anchor (by the top-visible
// calendar ID) across a data refresh, so edits and reloads don't jump the
// cursor or scroll.
func TestCalendarManagerRootSelectionRestoredByID(t *testing.T) {
	// Build a tall list so scrolling is meaningful.
	cals := map[int64]CalendarInfo{}
	for i := int64(1); i <= 30; i++ {
		cals[i] = CalendarInfo{Name: "Cal", DisplayOrder: i}
	}
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(60, 16)
	// Select calendar 20 and bring it into view.
	m = m.selectCalendar(20)
	selID, ok := m.selectedID()
	if !ok || selID != 20 {
		t.Fatalf("setup selection = %d ok=%v, want 20", selID, ok)
	}
	topBefore := m.list.offset

	// Refresh data: same calendars, fresh maps, plus one appended calendar.
	refreshed := map[int64]CalendarInfo{}
	for id, info := range cals {
		refreshed[id] = info
	}
	refreshed[31] = CalendarInfo{Name: "Tail", DisplayOrder: 31}
	m = m.SetData(refreshed, nil)

	selID, ok = m.selectedID()
	if !ok || selID != 20 {
		t.Fatalf("selection after refresh = %d ok=%v, want 20", selID, ok)
	}
	if m.list.offset != topBefore {
		t.Fatalf("scroll anchor changed: offset was %d, now %d", topBefore, m.list.offset)
	}
}

// TestCalendarManagerRootSelectionFallsBackWhenIDGone verifies that when the
// previously selected calendar disappears, the cursor lands on a valid row
// rather than going out of range.
func TestCalendarManagerRootSelectionFallsBackWhenIDGone(t *testing.T) {
	cals := flatManagerCalendars()
	m := newFlatManager().selectCalendar(3)
	delete(cals, 3)
	m = m.SetData(cals, nil)
	id, ok := m.selectedID()
	if !ok {
		t.Fatal("selection lost entirely after refresh")
	}
	if _, exists := m.calendars[id]; !exists {
		t.Fatalf("fallback selection %d not in current calendars", id)
	}
}

// TestCalendarManagerRootCloseEmitsClosedMsg verifies Esc and q both close the
// manager by emitting CalendarManagerClosedMsg.
func TestCalendarManagerRootCloseEmitsClosedMsg(t *testing.T) {
	m := newFlatManager()
	for name, key := range map[string]tea.KeyPressMsg{
		"esc": {Code: tea.KeyEscape},
		"q":   {Code: 'q', Text: "q"},
	} {
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("%s emitted no command", name)
		}
		if _, ok := cmd().(CalendarManagerClosedMsg); !ok {
			t.Fatalf("%s: expected CalendarManagerClosedMsg, got %T", name, cmd())
		}
	}
}

// TestCalendarManagerAddMenuOpensViaKeyAndClick verifies that both the `a` key
// and a click on the source + Add action open the manager-local menu, emit no
// app command, and put root focus on the + Add action.
func TestCalendarManagerAddMenuOpensViaKeyAndClick(t *testing.T) {
	t.Run("key", func(t *testing.T) {
		m := newFlatManager()
		opened, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		if cmd != nil {
			t.Fatalf("opening the menu emitted a command %T", cmd())
		}
		if !opened.addMenuOpen {
			t.Fatal("`a` did not open the add menu")
		}
		if opened.rootFocus != rootFocusAdd {
			t.Fatalf("opening via `a` left root focus %v, want add", opened.rootFocus)
		}
	})
	t.Run("click", func(t *testing.T) {
		m := newFlatManager()
		ax, ay, _, ok := m.sourceAddActionRect()
		if !ok {
			t.Fatal("source + Add action not present")
		}
		opened, cmd := m.Update(tea.MouseClickMsg{X: ax, Y: ay, Button: tea.MouseLeft})
		if cmd != nil {
			t.Fatalf("clicking Add emitted a command %T", cmd())
		}
		if !opened.addMenuOpen {
			t.Fatal("clicking + Add did not open the menu")
		}
		if opened.rootFocus != rootFocusAdd {
			t.Fatalf("opening via click left root focus %v, want add", opened.rootFocus)
		}
	})
}

// TestCalendarManagerAddMenuRowsAndNoCancel verifies the open menu renders the
// exact three rows and no Cancel affordance.
func TestCalendarManagerAddMenuRowsAndNoCancel(t *testing.T) {
	// Select the Local header so the inspector shows the summary rather than a
	// calendar edit-form preview, whose own Cancel button would trip the
	// menu-scoped Cancel assertion below.
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 0})
	m = m.openAddMenu()
	view := stripANSI(m.View())
	for _, want := range []string{"New Calendar…", "Add Account…", "Import Calendar File…"} {
		if !strings.Contains(view, want) {
			t.Errorf("menu missing row %q\n%s", want, view)
		}
	}
	if strings.Contains(view, "Cancel") {
		t.Errorf("menu must not render a Cancel row\n%s", view)
	}
}

// TestCalendarManagerAddMenuKeyboardClampsAndActivates verifies Up/Down clamp
// within the three rows and Enter emits the selected typed target and closes
// the menu.
func TestCalendarManagerAddMenuKeyboardClampsAndActivates(t *testing.T) {
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	for _, tc := range []struct {
		name   string
		keys   []tea.KeyPressMsg
		cursor int
		target CalendarManagerTarget
	}{
		{"default first", nil, 0, CalendarManagerTargetLocalCreate},
		{"down clamps at last", []tea.KeyPressMsg{down, down, down, down}, 2, CalendarManagerTargetImport},
		{"up clamps at first", []tea.KeyPressMsg{up, up}, 0, CalendarManagerTargetLocalCreate},
		{"second row", []tea.KeyPressMsg{down}, 1, CalendarManagerTargetAccountConnect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFlatManager().openAddMenu()
			for _, k := range tc.keys {
				m, _ = m.Update(k)
			}
			if m.addMenuCursor != tc.cursor {
				t.Fatalf("cursor = %d, want %d", m.addMenuCursor, tc.cursor)
			}
			m, cmd := m.Update(enter)
			if cmd == nil {
				t.Fatal("Enter emitted no command")
			}
			msg, ok := cmd().(CalendarManagerRequestedMsg)
			if !ok || msg.Target != tc.target {
				t.Fatalf("Enter target = %v, want %v", cmd(), tc.target)
			}
			if m.addMenuOpen {
				t.Error("menu still open after Enter")
			}
		})
	}
}

// TestCalendarManagerAddMenuTabShiftTabWrapCursor verifies Tab advances the
// menu cursor one row and wraps last→first, Shift-Tab reverses and wraps
// first→last, and neither key leaks into the root focus ring or the list
// selection underneath.
func TestCalendarManagerAddMenuTabShiftTabWrapCursor(t *testing.T) {
	m := newFlatManager()
	before, _ := m.selectedID()
	m = m.openAddMenu()
	if m.addMenuCursor != 0 {
		t.Fatalf("precondition: cursor=%d want 0", m.addMenuCursor)
	}

	// Forward: 0 → 1 → 2 → 0 (wraps last→first).
	for i, want := range []int{1, 2, 0} {
		next, _ := m.Update(managerTabKey(false))
		m = next
		if m.addMenuCursor != want {
			t.Fatalf("forward tab step %d: cursor=%d want %d", i, m.addMenuCursor, want)
		}
		if !m.addMenuOpen {
			t.Fatalf("forward tab step %d closed the menu", i)
		}
	}

	// Reverse: 0 → 2 → 1 → 0 (wraps first→last).
	for i, want := range []int{2, 1, 0} {
		next, _ := m.Update(managerTabKey(true))
		m = next
		if m.addMenuCursor != want {
			t.Fatalf("reverse shift+tab step %d: cursor=%d want %d", i, m.addMenuCursor, want)
		}
		if !m.addMenuOpen {
			t.Fatalf("reverse shift+tab step %d closed the menu", i)
		}
	}

	// Tab/Shift-Tab must stay inside the menu: no root focus cycle and no
	// selection change in the underlying list.
	if m.rootFocus != rootFocusAdd {
		t.Fatalf("menu tab moved root focus: got %v want add", m.rootFocus)
	}
	if after, _ := m.selectedID(); after != before {
		t.Fatalf("menu tab changed the list selection: before=%d after=%d", before, after)
	}
}

// TestCalendarManagerAddMenuSpaceActivatesTarget verifies Space activates the
// selected menu row, emits the same typed target as Enter, and closes the
// menu — routing through the shared activation binding rather than a
// menu-specific key code, so Space and Enter stay in lockstep.
func TestCalendarManagerAddMenuSpaceActivatesTarget(t *testing.T) {
	space := tea.KeyPressMsg{Code: ' ', Text: " "}
	want := []CalendarManagerTarget{
		CalendarManagerTargetLocalCreate,
		CalendarManagerTargetAccountConnect,
		CalendarManagerTargetImport,
	}
	for row, target := range want {
		m := newFlatManager().openAddMenu()
		// Walk to the target row with Down so the assertion is independent
		// of the default cursor position.
		for range row {
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		}
		activated, cmd := m.Update(space)
		if cmd == nil {
			t.Fatalf("row %d: Space emitted no command", row)
		}
		msg, ok := cmd().(CalendarManagerRequestedMsg)
		if !ok || msg.Target != target {
			t.Fatalf("row %d Space target = %v, want %v", row, cmd(), target)
		}
		if activated.addMenuOpen {
			t.Errorf("row %d: menu still open after Space", row)
		}
	}
}

// TestCalendarManagerAddMenuRowClickEmitsTarget verifies clicking each interior
// menu row emits the correct typed target, closes the menu, and leaves root
// focus on + Add so the return-to-root (after the host pushes a screen) lands
// back on the action.
func TestCalendarManagerAddMenuRowClickEmitsTarget(t *testing.T) {
	want := []CalendarManagerTarget{
		CalendarManagerTargetLocalCreate,
		CalendarManagerTargetAccountConnect,
		CalendarManagerTargetImport,
	}
	for row, target := range want {
		m := newFlatManager().openAddMenu()
		mx, my, _, _ := m.addMenuRect()
		clicked, cmd := m.Update(tea.MouseClickMsg{X: mx + 2, Y: my + 1 + row, Button: tea.MouseLeft})
		msg, ok := cmd().(CalendarManagerRequestedMsg)
		if !ok || msg.Target != target {
			t.Fatalf("row %d click = %v, want %v", row, cmd(), target)
		}
		if clicked.addMenuOpen {
			t.Errorf("row %d: menu still open after click", row)
		}
		if clicked.rootFocus != rootFocusAdd {
			t.Errorf("row %d: click left root focus %v, want add", row, clicked.rootFocus)
		}
	}
}

// TestCalendarManagerAddMenuOutsideClickDismissesWithoutClickThrough verifies
// a click outside the open menu dismisses it without emitting a command or
// activating the underlying list row.
func TestCalendarManagerAddMenuOutsideClickDismissesWithoutClickThrough(t *testing.T) {
	m := newFlatManager().openAddMenu()
	listX, listY, _, _ := m.listRegion()
	before, _ := m.selectedID()
	clicked, cmd := m.Update(tea.MouseClickMsg{X: listX + 8, Y: listY, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("outside click should not emit a command: %T", cmd())
	}
	if clicked.addMenuOpen {
		t.Fatal("outside click did not dismiss the menu")
	}
	if clicked.Screen() != CalendarManagerScreenList {
		t.Fatalf("outside click changed screen: %v", clicked.Screen())
	}
	if after, _ := clicked.selectedID(); after != before {
		t.Fatalf("outside click changed selection: before=%d after=%d", before, after)
	}
	if clicked.rootFocus != rootFocusAdd {
		t.Fatalf("outside click left root focus %v, want add", clicked.rootFocus)
	}
}

// TestCalendarManagerAddMenuEscDismissesWithoutClosingManager verifies Esc
// closes the menu without closing the manager or emitting a command.
func TestCalendarManagerAddMenuEscDismissesWithoutClosingManager(t *testing.T) {
	m := newFlatManager().openAddMenu()
	dismissed, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatalf("Esc should emit no command: %T", cmd())
	}
	if dismissed.addMenuOpen {
		t.Fatal("Esc did not dismiss the menu")
	}
	if dismissed.Screen() != CalendarManagerScreenList {
		t.Fatal("Esc dismissed the manager instead of the menu")
	}
	if dismissed.rootFocus != rootFocusAdd {
		t.Fatalf("Esc left root focus %v, want add", dismissed.rootFocus)
	}
}

// TestCalendarManagerAddMenuGeometryStaysInsideBox verifies the anchored menu
// rectangle stays fully inside the manager box at wide, narrow, and shallow
// terminal sizes.
func TestCalendarManagerAddMenuGeometryStaysInsideBox(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 40}, {narrowThreshold - 1, 30}, {100, 10}} {
		m := newFlatManager().SetSize(size.w, size.h).openAddMenu()
		boxW, boxH := m.boxSize()
		left := (size.w - boxW) / 2
		top := (size.h - boxH) / 2
		mx, my, mw, mh := m.addMenuRect()
		if mx < left+1 || mx+mw > left+boxW-1 {
			t.Errorf("size %dx%d: menu x [%d,%d) outside box [%d,%d)", size.w, size.h, mx, mx+mw, left+1, left+boxW-1)
		}
		if my < top+1 || my+mh > top+boxH-1 {
			t.Errorf("size %dx%d: menu y [%d,%d) outside box [%d,%d)", size.w, size.h, my, my+mh, top+1, top+boxH-1)
		}
	}
}

// TestCalendarManagerAddMenuBorderClickConsumedWithoutRouting verifies that a
// click on a menu border cell (the left/right │ columns or the rounded edges)
// is consumed: it neither activates a row nor dismisses the menu, and it never
// routes to the underlying list.
func TestCalendarManagerAddMenuBorderClickConsumedWithoutRouting(t *testing.T) {
	m := newFlatManager().openAddMenu()
	mx, my, mw, mh := m.addMenuRect()
	// Left/right border columns on an interior row must not activate.
	for _, x := range []int{mx, mx + mw - 1} {
		clicked, cmd := m.Update(tea.MouseClickMsg{X: x, Y: my + 1, Button: tea.MouseLeft})
		if cmd != nil {
			t.Fatalf("vertical border click at x=%d routed %T", x, cmd())
		}
		if !clicked.addMenuOpen {
			t.Fatalf("vertical border click at x=%d dismissed the menu", x)
		}
	}
	// Top/bottom border rows on an interior column must not activate.
	for _, y := range []int{my, my + mh - 1} {
		clicked, cmd := m.Update(tea.MouseClickMsg{X: mx + 2, Y: y, Button: tea.MouseLeft})
		if cmd != nil {
			t.Fatalf("horizontal border click at y=%d routed %T", y, cmd())
		}
		if !clicked.addMenuOpen {
			t.Fatalf("horizontal border click at y=%d dismissed the menu", y)
		}
	}
}

// TestCalendarManagerAddMenuNarrowWidthClampsInsideManager verifies that on a
// very narrow terminal—where the menu's natural width would overflow the
// box—the menu width is capped to the manager interior so the full rect stays
// inside the manager.
func TestCalendarManagerAddMenuNarrowWidthClampsInsideManager(t *testing.T) {
	m := newFlatManager().SetSize(28, 24).openAddMenu()
	boxW, boxH := m.boxSize()
	left := (28 - boxW) / 2
	top := (24 - boxH) / 2
	mx, my, mw, mh := m.addMenuRect()
	// The natural menu (28 cells) is wider than this box interior; the cap
	// must shrink it so the whole menu fits between the box borders.
	if mw > boxW-2 {
		t.Fatalf("menu width %d exceeds box interior %d", mw, boxW-2)
	}
	if mx < left+1 || mx+mw > left+boxW-1 {
		t.Fatalf("menu x [%d,%d) outside box [%d,%d)", mx, mx+mw, left+1, left+boxW-1)
	}
	if my < top+1 || my+mh > top+boxH-1 {
		t.Fatalf("menu y [%d,%d) outside box [%d,%d)", my, my+mh, top+1, top+boxH-1)
	}
}

// TestCalendarManagerRootNavigationMovesSelection verifies Up/Down move the
// selection and wrap-clamp at the ends.
func TestCalendarManagerRootNavigationMovesSelection(t *testing.T) {
	m := newFlatManager()
	if id, _ := m.selectedID(); id != 1 {
		t.Fatalf("initial selection = %d, want 1", id)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	identity, ok := m.list.currentIdentity()
	if !ok || identity.kind != accountHeaderRow || identity.id != 7 {
		t.Fatalf("after down = %+v, want Google account heading", identity)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if id, _ := m.selectedID(); id != 2 {
		t.Fatalf("after second down = %d, want 2", id)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	identity, ok = m.list.currentIdentity()
	if !ok || identity.kind != accountHeaderRow || identity.id != 7 {
		t.Fatalf("after up = %+v, want Google account heading", identity)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if id, _ := m.selectedID(); id != 1 {
		t.Fatalf("after second up = %d, want 1", id)
	}
}

// TestCalendarManagerDetailActionsTargetImmutableID verifies the pushed
// calendar detail exposes Export, Set Default, and Delete as leading actions
// that all carry the selected calendar's immutable ID, with Delete styled as
// the destructive variant.
func TestCalendarManagerDetailActionsTargetImmutableID(t *testing.T) {
	// Account calendar: no Delete (footnote explains ownership instead).
	m := newFlatManager().selectCalendar(3) // Holidays, account 7, not default
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if mm.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	buttons := mm.calendarForm.form.actionButtons
	labels := make([]string, 0, len(buttons))
	for _, b := range buttons {
		labels = append(labels, b.Label)
	}
	if got, want := strings.Join(labels, ","), "Set as Default,Export Calendar…"; got != want {
		t.Fatalf("remote detail actions = %q, want %q", got, want)
	}
	if view := stripANSI(mm.calendarForm.View()); !strings.Contains(view, "lives in your Google account") {
		t.Errorf("remote detail missing account-ownership footnote:\n%s", view)
	}

	// Local calendar: Delete remains, targeting the immutable ID.
	m = newFlatManager().selectCalendar(1) // On device, local
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if mm.calendarForm == nil {
		t.Fatal("Enter did not push a local calendar form")
	}
	buttons = mm.calendarForm.form.actionButtons
	labels = labels[:0]
	for _, b := range buttons {
		labels = append(labels, b.Label)
	}
	if got, want := strings.Join(labels, ","), "Set as Default,Export Calendar…,Delete Calendar…"; got != want {
		t.Fatalf("local detail actions = %q, want %q", got, want)
	}
	for _, b := range buttons {
		msg := b.OnPress()
		switch msg := msg.(type) {
		case CalendarSetDefaultRequestedMsg:
			if msg.ID != 1 || msg.Name != "On device" {
				t.Errorf("Set Default = %+v, want ID 1", msg)
			}
		case CalendarExportRequestedMsg:
			if msg.ID != 1 || msg.Name != "On device" {
				t.Errorf("Export = %+v, want ID 1", msg)
			}
		case CalendarDeleteRequestedMsg:
			if msg.ID != 1 || msg.Name != "On device" {
				t.Errorf("Delete = %+v, want ID 1", msg)
			}
		default:
			t.Errorf("unexpected action message %T from %q", msg, b.Label)
		}
		if b.Label == "Delete Calendar…" && b.Variant != ButtonDanger {
			t.Errorf("Delete variant = %v, want ButtonDanger", b.Variant)
		}
		if b.Label != "Delete Calendar…" && b.Variant != Button {
			t.Errorf("%q variant = %v, want Button", b.Label, b.Variant)
		}
	}
}

func TestCalendarManagerRootSpaceTogglesBothDirections(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	if m.hidden[2] {
		t.Fatal("calendar 2 should start visible")
	}
	// Visible -> hidden.
	m1, cmd := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 2 || !msg.Hidden {
		t.Fatalf("visible->hidden: msg = %+v, want {ID:2 Hidden:true}", msg)
	}
	if !m1.hidden[2] {
		t.Error("local hidden state not flipped to true")
	}
	if row := managerCalendarLine(t, m1, 2); !strings.HasPrefix(row, "○") {
		t.Errorf("row did not flip to the hidden outline circle: %q", row)
	}
	// Hidden -> visible.
	m2, cmd := m1.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	msg, ok = cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 2 || msg.Hidden {
		t.Fatalf("hidden->visible: msg = %+v, want {ID:2 Hidden:false}", msg)
	}
	if m2.hidden[2] {
		t.Error("local hidden state not flipped back to false")
	}
	if row := managerCalendarLine(t, m2, 2); !strings.HasPrefix(row, "●") {
		t.Errorf("row did not flip back to the filled visibility circle: %q", row)
	}
}

// TestCalendarManagerRootSetDataClampsWhenSelectedTailRemoved verifies that
// removing the selected tail calendar leaves the cursor on a valid surviving
// row instead of out of range.
func TestCalendarManagerRootSetDataClampsWhenSelectedTailRemoved(t *testing.T) {
	cals := flatManagerCalendars() // canonical order [1, 2, 3, 4]
	m := newFlatManager().selectCalendar(4)
	if id, _ := m.selectedID(); id != 4 {
		t.Fatalf("setup: selected %d, want 4", id)
	}
	delete(cals, 4)
	m = m.SetData(cals, nil)
	id, ok := m.selectedID()
	if !ok {
		t.Fatal("selectedID out of range after removing selected tail")
	}
	if m.list.cursor < 0 || m.list.cursor >= len(m.list.rows) {
		t.Fatalf("cursor %d out of range [0,%d) after refresh", m.list.cursor, len(m.list.rows))
	}
	if _, exists := m.calendars[id]; !exists {
		t.Fatalf("fallback selection %d not in current calendars", id)
	}
}

// TestCalendarManagerRootMouseRejectsClicksOutsideListWidth verifies a click
// on the right row's Y but past the list's right edge does not select — the
// hit-test bounds the list column on both sides, not just the left.
func TestCalendarManagerRootMouseRejectsClicksOutsideListWidth(t *testing.T) {
	m := newFlatManager()
	lx, listY, lw, _ := m.listRegion()
	before, _ := m.selectedID()
	clicked, cmd := m.Update(tea.MouseClickMsg{X: lx + lw + 5, Y: listY + 2, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("out-of-width click should not emit a command: %T", cmd())
	}
	after, _ := clicked.selectedID()
	if after != before {
		t.Fatalf("out-of-width click changed selection: before=%d after=%d", before, after)
	}
}

// TestCalendarManagerRootOverflowKeepsLastSelectedRowVisible is the regression
// for the clampedScroll max-clamp bug: when the list overflows, the indicator
// reserves one row (contentH = h-1), but the max-scroll clamp used the full
// viewport height h. That off-by-one let the indicator overwrite the selected
// last row, hiding it and making it unclickable. The clamp must use contentH
// so the last data row stays rendered and reachable above the indicator.
func TestCalendarManagerRootOverflowKeepsLastSelectedRowVisible(t *testing.T) {
	cals := map[int64]CalendarInfo{}
	for i := int64(1); i <= 12; i++ {
		cals[i] = CalendarInfo{Name: fmt.Sprintf("Cal%02d", i), DisplayOrder: i}
	}
	// SetSize(60,16) -> narrow box, bodyH = 7, so 12 calendars overflow and
	// reserve the last visible line for the scroll indicator.
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(60, 16)
	m = m.selectCalendar(12)
	if id, _ := m.selectedID(); id != 12 {
		t.Fatalf("setup: selected %d, want 12", id)
	}

	// The selected last row must still be rendered, not overwritten by the
	// overflow indicator.
	view := stripANSI(m.View())
	if !strings.Contains(view, "Cal12") {
		t.Errorf("selected last row Cal12 missing from view (overwritten by indicator?)\n%s", view)
	}

	// ... and it must remain clickable in the grouped list viewport.
	listX, listY, _, listH := m.listRegion()
	row := calendarListRowForCalendarID(t, m.list, 12) - m.list.offset
	if row < 0 || row >= listH {
		t.Fatalf("selected last row viewport position = %d, height %d", row, listH)
	}
	opened, _ := m.Update(tea.MouseClickMsg{X: listX + 8, Y: listY + row, Button: tea.MouseLeft})
	if opened.calendarForm == nil || opened.calendarForm.Draft().ID != 12 {
		t.Error("selected last row Cal12 is not mouse-clickable")
	}
}

// calendarDetailFieldIndex returns the form item index of the first field of
// the given kind ("opener" or "checkbox") in the manager's active calendar
// detail, or -1 when no detail is open or no such field exists.
func calendarDetailFieldIndex(m CalendarManagerModel, kind string) int {
	if m.calendarForm == nil {
		return -1
	}
	form := m.calendarForm.form
	for i := range form.ItemCount() {
		switch form.Field(i).(type) {
		case *OpenerField:
			if kind == "opener" {
				return i
			}
		case *CheckboxField:
			if kind == "checkbox" {
				return i
			}
		}
	}
	return -1
}

// focusCalendarDetailField moves the calendar detail's form focus onto the
// first field of the given kind. Tests use this instead of driving Tab so the
// assertion is independent of how many fields precede the target.
func focusCalendarDetailField(m CalendarManagerModel, kind string) (CalendarManagerModel, bool) {
	idx := calendarDetailFieldIndex(m, kind)
	if idx < 0 {
		return m, false
	}
	form := m.calendarForm.form
	form, _ = form.focusIndex(idx)
	m.calendarForm.form = form
	return m, true
}

// calendarDetailCheckboxClick renders the active calendar detail to populate
// the shared mouse tracker, then resolves the Display Calendar checkbox zone
// into a terminal-space MouseClickMsg that hits it. Tests use it to exercise
// the mouse path of the visibility toggle without hard-coding geometry.
func calendarDetailCheckboxClick(m CalendarManagerModel, cbIdx int) (tea.MouseClickMsg, bool) {
	if m.calendarForm == nil {
		return tea.MouseClickMsg{}, false
	}
	_ = m.View() // populate manager-shell mouse zones
	bw, bh := m.BoxSize()
	ox := (m.width - bw) / 2
	oy := (m.height - bh) / 2
	target := fieldTarget(cbIdx)
	for _, z := range defaultMouseTracker.zones {
		if z.name != target {
			continue
		}
		x := ox + (z.startX+z.endX)/2
		y := oy + z.startY
		return tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y}, true
	}
	return tea.MouseClickMsg{}, false
}

// TestCalendarManagerDetailBackRestoresRootSelection verifies pushing a
// calendar detail and pressing Back (Esc) returns to the root list with the
// same calendar selected by immutable ID.
func TestCalendarManagerDetailBackRestoresRootSelection(t *testing.T) {
	m := newFlatManager().selectCalendar(3)
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	closing, cmd := pushed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc emitted no close command")
	}
	popped, _ := closing.Update(cmd())
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List", popped.Screen())
	}
	if popped.calendarForm != nil {
		t.Fatal("calendar form was not cleared on pop")
	}
	if id, ok := popped.selectedID(); !ok || id != 3 {
		t.Fatalf("root selection after Back = %d ok=%v, want 3", id, ok)
	}
}

// TestCalendarManagerDetailBackRestoresRootScroll verifies the scroll offset
// survives a push/pop cycle by ID, so opening and closing a detail does not
// jump the list back to the top.
func TestCalendarManagerDetailBackRestoresRootScroll(t *testing.T) {
	cals := map[int64]CalendarInfo{}
	for i := int64(1); i <= 30; i++ {
		cals[i] = CalendarInfo{Name: fmt.Sprintf("Cal %d", i), Color: "#a6e3a1", DisplayOrder: i}
	}
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(50, 16)
	// Move the cursor well past the first viewport so the list scrolls.
	for range 18 {
		m.list = m.list.moveCursor(1)
	}
	start := m.list.offset
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	closing, cmd := pushed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc emitted no close command")
	}
	popped, _ := closing.Update(cmd())
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List", popped.Screen())
	}
	gotStart := popped.list.offset
	if gotStart != start {
		t.Fatalf("scroll top = %d after Back, want %d", gotStart, start)
	}
}

// TestCalendarManagerDetailLocalHasLocationOnly verifies a local calendar's
// detail renders a labeled Location row valued Local and has no Account
// opener (local calendars have no owning account to drill into).
func TestCalendarManagerDetailLocalHasLocationOnly(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, local
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	view := stripANSI(pushed.calendarForm.View())
	if !strings.Contains(view, "Location") || !strings.Contains(view, "Local") {
		t.Errorf("local detail missing labeled Location row:\n%s", view)
	}
	if calendarDetailFieldIndex(pushed, "opener") >= 0 {
		t.Errorf("local detail should not expose an Account opener:\n%s", view)
	}
}

// TestCalendarManagerDetailRemoteHasAccountOpener verifies a remote calendar's
// detail renders an actionable Account row — the account name plus drill-in
// chevron in the shared label column — that, when activated, pushes the
// owning account's settings onto the stack.
func TestCalendarManagerDetailRemoteHasAccountOpener(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google, account 7
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	view := stripANSI(pushed.calendarForm.View())
	if !strings.Contains(view, "Account") || !strings.Contains(view, "Google ›") {
		t.Errorf("remote detail missing actionable Account row:\n%s", view)
	}
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	// The opener does not push the account detail itself: it passes the
	// canonical AccountSettingsRequestedMsg to the host, which owns the
	// account record and later calls OpenAccount with full params. The
	// calendar detail stays put underneath.
	opened, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Account opener emitted no request")
	}
	req, ok := cmd().(AccountSettingsRequestedMsg)
	if !ok || req.AccountID != 7 {
		t.Fatalf("Account opener = %#v, want AccountSettingsRequestedMsg{AccountID:7}", cmd())
	}
	if opened.Screen() != CalendarManagerScreenCalendar || opened.accountSettings != nil {
		t.Fatalf("opener should not push account detail: screen=%v account=%v", opened.Screen(), opened.accountSettings)
	}
}

func TestCalendarManagerDetailAccountBackPreservesDraft(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	pushed.calendarForm.form.Field(cdIdxName).(*TextField).SetValue("Edited Primary")

	// The opener passes AccountSettingsRequestedMsg to the host; the host
	// (Task 4) resolves the canonical account record and calls OpenAccount
	// with full params — provider/server/username/attention preserved.
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	requested, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	req, ok := cmd().(AccountSettingsRequestedMsg)
	if !ok || req.AccountID != 7 {
		t.Fatalf("opener request = %#v, want AccountSettingsRequestedMsg{AccountID:7}", cmd())
	}
	opened := requested.OpenAccount(AccountSettingsParams{
		AccountID:      7,
		DisplayName:    "Personal Google",
		Provider:       "Google Account",
		ServerURL:      "https://apidata.googleusercontent.com/caldav/v2/",
		Username:       "douglas@example.com",
		CalendarCount:  2,
		AttentionCount: 1,
		AuthType:       "oauth2",
	})
	if opened.Screen() != CalendarManagerScreenAccount {
		t.Fatalf("screen = %v, want Account", opened.Screen())
	}
	if p := opened.accountSettings.Params(); p.Provider != "Google Account" ||
		p.ServerURL != "https://apidata.googleusercontent.com/caldav/v2/" ||
		p.Username != "douglas@example.com" || p.AttentionCount != 1 {
		t.Fatalf("canonical account params not preserved: %+v", p)
	}
	// Back returns to the originating calendar detail with its unsaved draft
	// intact — the form is never reconstructed.
	closing, cmd := opened.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc emitted no account close command")
	}
	popped, _ := closing.Update(cmd())
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar after Back", popped.Screen())
	}
	if got := popped.calendarForm.Draft().Name; got != "Edited Primary" {
		t.Fatalf("calendar draft after Account Back = %q, want %q", got, "Edited Primary")
	}
}

// TestCalendarManagerDetailVisibilityToggleEmitsDesiredState verifies the
// detail's Display Calendar toggle emits CalendarVisibilityToggledMsg with the
// desired Hidden state immediately, and mirrors the change into the root's
// hidden map so the dot stays consistent on Back.
func TestCalendarManagerDetailVisibilityToggleEmitsDesiredState(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, visible
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.hidden[1] {
		t.Fatal("calendar 1 should start visible")
	}
	focused, ok := focusCalendarDetailField(pushed, "checkbox")
	if !ok {
		t.Fatal("detail has no Display Calendar checkbox")
	}
	hidden, cmd := focused.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("visibility toggle emitted no command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 1 || !msg.Hidden {
		t.Fatalf("toggle msg = %+v, want {ID:1 Hidden:true}", msg)
	}
	hidden, _ = hidden.Update(msg)
	if !hidden.hidden[1] {
		t.Error("root hidden map not mirrored to true")
	}
	// Toggling back emits the opposite desired state.
	visible, cmd := hidden.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	msg, ok = cmd().(CalendarVisibilityToggledMsg)
	if !ok || msg.ID != 1 || msg.Hidden {
		t.Fatalf("toggle back msg = %+v, want {ID:1 Hidden:false}", msg)
	}
	visible, _ = visible.Update(msg)
	if visible.hidden[1] {
		t.Error("root hidden map not mirrored back to false")
	}
}

// TestCalendarManagerDetailLeftPopsToRoot verifies the Left arrow pops a
// pushed calendar detail back to the root list (a Back gesture) when the
// focus is not on a text-editing field. Root Left is unchanged (a no-op).
func TestCalendarManagerDetailLeftPopsToRoot(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	// Focus the Account opener (a non-editing field) so Left is free to pop.
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	popped, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Fatalf("Left pop should emit no command, got %T", cmd())
	}
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List after Left", popped.Screen())
	}
	if popped.calendarForm != nil {
		t.Fatal("calendar form was not cleared on Left pop")
	}
	// Root Left is a no-op (no root binding consumes it).
	rootAgain, cmd := popped.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil || rootAgain.Screen() != CalendarManagerScreenList {
		t.Fatalf("root Left should be a no-op: cmd=%v screen=%v", cmd, rootAgain.Screen())
	}
}

// TestCalendarManagerDetailLeftKeepsDirtyDraft verifies the Back gesture
// never discards an unsaved draft: with edited metadata and focus on a
// non-editing field, Left leaves the editor mounted with the edit intact.
// Esc (Cancel) remains the explicit discard.
func TestCalendarManagerDetailLeftKeepsDirtyDraft(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Type into the focused Name field to dirty the draft.
	pushed, _ = pushed.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if pushed.calendarForm == nil || !pushed.calendarForm.dirtyMetadata() {
		t.Fatal("typing into Name did not dirty the draft")
	}
	dirtyName := pushed.calendarForm.Draft().Name
	// Move focus to the Account opener so Left is not a cursor move.
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	kept, _ := focused.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if kept.Screen() != CalendarManagerScreenCalendar || kept.calendarForm == nil {
		t.Fatalf("Left discarded a dirty draft: screen=%v", kept.Screen())
	}
	if got := kept.calendarForm.Draft().Name; got != dirtyName {
		t.Fatalf("draft name = %q, want %q preserved", got, dirtyName)
	}
}

// TestCalendarManagerDetailButtonDisposition pins the Apple-sheet action
// layout in a wide editor: Set as Default and Export share the utility tier
// on one row, while Delete sits flush-left on the same line as the
// right-aligned Save and Cancel commit controls.
func TestCalendarManagerDetailButtonDisposition(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // local: all three actions
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	rows := strings.Split(stripANSI(m.calendarForm.form.ButtonRowView()), "\n")
	if len(rows) != 3 {
		t.Fatalf("button block = %d rows, want tier, blank, commit:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[0], "Set as Default") || !strings.Contains(rows[0], "Export Calendar…") {
		t.Fatalf("utility tier row = %q, want Set as Default beside Export", rows[0])
	}
	if strings.TrimSpace(rows[1]) != "" {
		t.Fatalf("row between tiers = %q, want blank", rows[1])
	}
	commit := rows[2]
	if !strings.Contains(commit, "Delete Calendar…") || !strings.Contains(commit, "Save") || !strings.Contains(commit, "Cancel") {
		t.Fatalf("commit row = %q, want Delete flush-left beside Save/Cancel", commit)
	}
	if strings.Index(commit, "Delete Calendar…") > strings.Index(commit, "Save") {
		t.Fatalf("Delete must render left of Save: %q", commit)
	}
}

// TestCalendarManagerTabTraversalRoundTripsThroughEditor verifies Tab
// traversal is continuous across the whole dialog: from the source list,
// repeated Tab enters the previewed editor, walks its fields and buttons,
// and exits past the last control back to the focused source list; Shift-Tab
// from the editor's first field exits back to + Add.
func TestCalendarManagerTabTraversalRoundTripsThroughEditor(t *testing.T) {
	tab := managerTabKey(false)
	m := newFlatManager().selectCalendar(1)

	// list → + Add → editor (first field focused).
	m, _ = m.Update(tab)
	m, _ = m.Update(tab)
	if m.Screen() != CalendarManagerScreenCalendar || m.calendarForm == nil {
		t.Fatalf("tab did not enter the editor: screen=%v", m.Screen())
	}
	if got, want := m.calendarForm.form.Focused(), m.calendarForm.form.FirstFocusable(); got != want {
		t.Fatalf("editor entry focus = %d, want first focusable %d", got, want)
	}

	// Walk the whole form: Tab from every slot until the last focusable.
	for guard := 0; m.calendarForm.form.Focused() != m.calendarForm.form.LastFocusable(); guard++ {
		if guard > 32 {
			t.Fatal("tab never reached the form's last focusable slot")
		}
		m, _ = m.Update(tab)
		if m.calendarForm == nil {
			t.Fatal("tab exited the editor before its last control")
		}
	}

	// Tab past the last control exits to the focused source list.
	m, _ = m.Update(tab)
	if m.Screen() != CalendarManagerScreenList || m.calendarForm != nil {
		t.Fatalf("tab past the last control did not return to the list: screen=%v", m.Screen())
	}
	if m.rootFocus != rootFocusList || !m.list.Focused() {
		t.Fatalf("root focus = %v after exiting the editor, want focused list", m.rootFocus)
	}

	// Shift-Tab from the editor's first field exits back to + Add.
	m, _ = m.Update(tab)
	m, _ = m.Update(tab) // re-enter the editor
	if m.calendarForm == nil {
		t.Fatal("tab did not re-enter the editor")
	}
	m, _ = m.Update(managerTabKey(true))
	if m.Screen() != CalendarManagerScreenList || m.rootFocus != rootFocusAdd {
		t.Fatalf("shift-tab from first field: screen=%v focus=%v, want list screen with + Add focus", m.Screen(), m.rootFocus)
	}
}

// TestCalendarManagerTabStaysInsideDirtyEditor verifies Tab keeps wrapping
// inside the form once the draft is dirty, so traversal can never discard
// typed edits.
func TestCalendarManagerTabStaysInsideDirtyEditor(t *testing.T) {
	tab := managerTabKey(false)
	m := newFlatManager().selectCalendar(1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // dirty the Name field

	// Tab far past the form's slot count: the editor must stay mounted.
	for range 40 {
		m, _ = m.Update(tab)
		if m.calendarForm == nil {
			t.Fatal("tab exited a dirty editor")
		}
	}
	if m.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar while draft is dirty", m.Screen())
	}
}

// TestCalendarManagerEscOnDirtyDraftAsksBeforeDiscarding verifies the
// Apple-style save-changes flow: Esc on a dirty calendar draft opens a
// destructive Discard prompt instead of closing; declining keeps the draft
// intact and confirming pops to the root list.
func TestCalendarManagerEscOnDirtyDraftAsksBeforeDiscarding(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // dirty the Name field
	dirtyName := m.calendarForm.Draft().Name

	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc emitted no close command")
	}
	m, _ = m.Update(cmd())
	if m.discardConfirm == nil {
		t.Fatal("esc on a dirty draft did not open the discard prompt")
	}
	if m.Screen() != CalendarManagerScreenCalendar || m.calendarForm == nil {
		t.Fatalf("prompt must keep the editor mounted: screen=%v", m.Screen())
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Discard unsaved changes?") || !strings.Contains(view, "Discard") {
		t.Fatalf("discard prompt not rendered:\n%s", view)
	}

	// Keep Editing: the prompt closes, the draft survives.
	kept, _ := m.Update(ConfirmDialogResultMsg{Confirmed: false})
	if kept.discardConfirm != nil || kept.calendarForm == nil {
		t.Fatal("declining the prompt must return to the editor")
	}
	if got := kept.calendarForm.Draft().Name; got != dirtyName {
		t.Fatalf("draft name = %q, want %q preserved", got, dirtyName)
	}

	// Discard: the prompt closes and the editor pops to the root list.
	discarded, _ := m.Update(ConfirmDialogResultMsg{Confirmed: true})
	if discarded.discardConfirm != nil || discarded.calendarForm != nil || discarded.Screen() != CalendarManagerScreenList {
		t.Fatalf("confirming discard did not pop to the list: screen=%v", discarded.Screen())
	}
}

// TestCalendarManagerEscOnCleanDraftClosesWithoutPrompt verifies an unedited
// form still closes on Esc with no intervening prompt.
func TestCalendarManagerEscOnCleanDraftClosesWithoutPrompt(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc emitted no close command")
	}
	m, _ = m.Update(cmd())
	if m.discardConfirm != nil {
		t.Fatal("clean draft must not prompt on close")
	}
	if m.Screen() != CalendarManagerScreenList || m.calendarForm != nil {
		t.Fatalf("clean esc did not close the editor: screen=%v", m.Screen())
	}
}

// TestCalendarManagerPickerLeftKeepsStagedSelection verifies Left pops an
// untouched account-calendars picker but never one holding staged,
// unapplied subscription changes.
func TestCalendarManagerPickerLeftKeepsStagedSelection(t *testing.T) {
	// Untouched picker: Left pops back to the root list.
	m := newFlatManager().OpenAccountCalendars(pickerDiscovery())
	popped, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("Left did not pop a clean picker: screen=%v", popped.Screen())
	}

	// Staged change: Left must keep the picker and its selection mounted.
	m = newFlatManager().OpenAccountCalendars(pickerDiscovery())
	m.accountPicker.selected["/primary/"] = true
	if !m.accountPicker.dirtySelection() {
		t.Fatal("staged toggle did not mark the picker dirty")
	}
	kept, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if kept.Screen() != CalendarManagerScreenAccountCalendars || kept.accountPicker == nil {
		t.Fatalf("Left discarded staged picker changes: screen=%v", kept.Screen())
	}
	if !kept.accountPicker.selected["/primary/"] {
		t.Fatal("staged selection lost after Left")
	}
}

// TestCalendarManagerDetailLeftDoesNotStealCursor verifies Left keeps moving
// the cursor while a text field is focused, so the Back gesture never
// interrupts editing.
func TestCalendarManagerDetailLeftDoesNotStealCursor(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, local
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	// Name (a TextField) is focused by default after the detail opens.
	popped, _ := pushed.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("Left popped the detail while editing: screen=%v", popped.Screen())
	}
	if popped.calendarForm == nil {
		t.Fatal("Left discarded the calendar detail while editing")
	}
}

// TestCalendarManagerDetailLeftPopsAccountToCalendar verifies Left pops the
// pushed account detail back to the originating calendar detail.
func TestCalendarManagerDetailLeftPopsAccountToCalendar(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m.OpenAccount(AccountSettingsParams{
		AccountID:   7,
		DisplayName: "Personal Google",
		Provider:    "Google Account",
		AuthType:    "oauth2",
	})
	if m.Screen() != CalendarManagerScreenAccount {
		t.Fatalf("screen = %v, want Account", m.Screen())
	}
	popped, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Fatalf("Left pop should emit no command, got %T", cmd())
	}
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar after Left", popped.Screen())
	}
	if popped.accountSettings != nil {
		t.Fatal("account settings was not cleared on Left pop")
	}
}

// TestCalendarManagerDetailVisibilityMouseToggleEmitsDesiredState verifies a
// mouse click on the Display Calendar checkbox emits the same
// CalendarVisibilityToggledMsg as the keyboard path (regression: the mouse
// branch used to return before the pre/post visibility comparison).
func TestCalendarManagerDetailVisibilityMouseToggleEmitsDesiredState(t *testing.T) {
	m := newFlatManager().selectCalendar(1).SetSize(120, 40)
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil || pushed.hidden[1] {
		t.Fatal("precondition: calendar 1 detail open and visible")
	}
	cbIdx := calendarDetailFieldIndex(pushed, "checkbox")
	if cbIdx < 0 {
		t.Fatal("detail has no Display Calendar checkbox")
	}
	click, ok := calendarDetailCheckboxClick(pushed, cbIdx)
	if !ok {
		t.Fatal("could not resolve checkbox click point")
	}
	hidden, cmd := pushed.Update(click)
	if cmd == nil {
		t.Fatal("mouse visibility toggle emitted no command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 1 || !msg.Hidden {
		t.Fatalf("mouse toggle msg = %+v, want {ID:1 Hidden:true}", msg)
	}
	hidden, _ = hidden.Update(msg)
	if !hidden.hidden[1] {
		t.Error("root hidden map not mirrored to true after mouse toggle")
	}
}

func TestCalendarManagerAccountConnectionLeftNeverOpensRoot(t *testing.T) {
	for _, focus := range []int{calDAVIdxAuth, -1} {
		m := newFlatManager().OpenAccountConnection()
		if focus < 0 {
			focus = m.calendarForm.form.cancelIndex()
		}
		m.calendarForm.form.focused = focus
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
		if updated.Screen() != CalendarManagerScreenCalendar || updated.calendarForm == nil {
			t.Fatalf("Left at focus %d exposed manager root: screen=%v", focus, updated.Screen())
		}
	}
}

func TestCalendarManagerDirectAccountCloseReturnsToList(t *testing.T) {
	for _, press := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: tea.KeyLeft},
	} {
		m := newFlatManager().OpenAccount(AccountSettingsParams{
			AccountID: 7, DisplayName: "Personal Google", AuthType: "oauth2",
		})
		closing, cmd := m.Update(press)
		if press.Code == tea.KeyLeft {
			if cmd != nil || closing.Screen() != CalendarManagerScreenList {
				t.Fatalf("Left did not return directly to list: screen=%v cmd=%v", closing.Screen(), cmd)
			}
			continue
		}
		if cmd == nil {
			t.Fatal("Esc emitted no account close command")
		}
		closed, _ := closing.Update(cmd())
		if closed.Screen() != CalendarManagerScreenList || closed.accountSettings != nil {
			t.Fatalf("Esc did not restore list: screen=%v", closed.Screen())
		}
	}
}

type managerCommandProbeField struct {
	executed *bool
}

func (f *managerCommandProbeField) Update(tea.Msg) tea.Cmd {
	return func() tea.Msg {
		*f.executed = true
		return nil
	}
}
func (*managerCommandProbeField) View() string      { return "" }
func (*managerCommandProbeField) Focus() tea.Cmd    { return nil }
func (*managerCommandProbeField) Blur()             {}
func (*managerCommandProbeField) SetWidth(int)      {}
func (*managerCommandProbeField) IsFocusable() bool { return true }

func TestCalendarManagerDoesNotExecuteChildCommandsSynchronously(t *testing.T) {
	executed := false
	m := newFlatManager().OpenCalendar(CalendarDialogParams{ID: 1, Name: "On device"})
	m.calendarForm.form.items[0].Field = &managerCommandProbeField{executed: &executed}
	m.calendarForm.form.focused = 0

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if executed {
		t.Fatal("manager executed child command inside Update")
	}
	if cmd == nil {
		t.Fatal("manager dropped child command")
	}
	if updated.screen != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want calendar inspector", updated.screen)
	}
	_ = cmd()
	if !executed {
		t.Fatal("returned child command did not execute when Bubble Tea ran it")
	}
}

func TestCalendarManagerRootGroupsAccountsAndShowsInspector(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	view := stripANSI(m.View())
	for _, want := range []string{
		"Local", "Google", "Fastmail",
		"● Primary",
		"Edit calendar", "Name", "Save",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("manager view missing %q:\n%s", want, view)
		}
	}
	// The manager renders account headings as plain section titles: no
	// disclosure chevron in front of the account name.
	if strings.Contains(view, "▾") || strings.Contains(view, "▸") {
		t.Errorf("manager account headings must not show disclosure chevrons:\n%s", view)
	}
	if strings.Contains(view, "Edit ›") {
		t.Fatalf("calendar rows must not repeat Edit links beside the inspector:\n%s", view)
	}

	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	accountView := stripANSI(m.View())
	for _, want := range []string{"Google", "Calendars", "2", "Account Settings…"} {
		if !strings.Contains(accountView, want) {
			t.Errorf("account inspector missing %q:\n%s", want, accountView)
		}
	}
}

// inspectorActionScreenRow maps the action's screen y into a View() row index
// so tests can read the rendered button without re-deriving the box geometry.
func inspectorActionScreenRow(m CalendarManagerModel, ay int) int {
	_, boxH := m.boxSize()
	dialogY := (m.height - boxH) / 2
	return ay - dialogY
}

// TestCalendarManagerInspectorHeaderIsCalendarsNotAdd verifies the manager
// header is the plain "Calendars" title and never couples the + Add action.
func TestCalendarManagerInspectorHeaderIsCalendarsNotAdd(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	for _, line := range strings.Split(stripANSI(m.View()), "\n") {
		if strings.Contains(line, "Calendars") && strings.Contains(line, "+ Add") {
			t.Fatalf("header couples Calendars and + Add: %q", line)
		}
	}
	if _, _, pw, ph, ok := m.previewPaneRect(); !ok || pw == 0 || ph == 0 {
		t.Fatalf("calendar selection has no preview pane: ok=%v w=%d h=%d", ok, pw, ph)
	}
}

// TestCalendarManagerInspectorCalendarShowsEditFormPreview verifies selecting
// a calendar renders its edit form immediately in the inspector (macOS
// Settings-style master–detail): fields, Display checkbox, and buttons appear
// with no Edit… pill and no pinned-action rect in between.
func TestCalendarManagerInspectorCalendarShowsEditFormPreview(t *testing.T) {
	m := newFlatManager().selectCalendar(3) // Holidays, Google
	view := stripANSI(m.View())
	for _, want := range []string{"Edit calendar", "Name", "Holidays", "Display calendar", "Save"} {
		if !strings.Contains(view, want) {
			t.Errorf("calendar preview missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Edit…") {
		t.Errorf("calendar selection must render the form preview, not an Edit… pill:\n%s", view)
	}
	if _, _, _, ok := m.inspectorActionRect(); ok {
		t.Fatal("calendar selection must not expose a pinned-action rect")
	}
	if _, _, _, _, ok := m.previewPaneRect(); !ok {
		t.Fatal("calendar selection must expose the preview pane rect")
	}
}

// TestCalendarManagerInspectorAccountShowsAccountSettingsAction verifies a
// remote account inspector shows the account name, metadata, and a bottom
// Account Settings… action — and no legacy "Enter  Account Settings".
func TestCalendarManagerInspectorAccountShowsAccountSettingsAction(t *testing.T) {
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	view := stripANSI(m.View())
	for _, want := range []string{"Google", "Calendars", "2", "Account Settings…"} {
		if !strings.Contains(view, want) {
			t.Errorf("account inspector missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Enter  Account Settings") {
		t.Errorf("legacy underlined Account Settings pseudo-link still rendered:\n%s", view)
	}
	_, ay, _, ok := m.inspectorActionRect()
	if !ok {
		t.Fatal("account inspector has no action rect")
	}
	row := inspectorActionScreenRow(m, ay)
	if !strings.Contains(strings.Split(view, "\n")[row], "Account Settings…") {
		t.Fatalf("Account Settings… not on action row %d:\n%s", row, view)
	}
}

// TestCalendarManagerInspectorLocalShowsCountWithoutAction verifies the Local
// group inspector shows "On this device" with its calendar count but no
// bottom action (no Add, no Account Settings) and no action rect.
func TestCalendarManagerInspectorLocalShowsCountWithoutAction(t *testing.T) {
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 0})
	// The Local inspector pane shows "On this device" and its calendar count;
	// the unwanted-action check is scoped to the inspector pane because the
	// source column carries its own (unrelated) + Add action.
	w, h := m.inspectorPaneSize()
	inspector := stripANSI(strings.Join(m.selectionInspectorLines(w, h), "\n"))
	for _, want := range []string{"Local", "On this device", "Calendars", "1"} {
		if !strings.Contains(inspector, want) {
			t.Errorf("Local inspector missing %q:\n%s", want, inspector)
		}
	}
	for _, unwanted := range []string{"Account Settings…", "Edit…", "Add Calendar"} {
		if strings.Contains(inspector, unwanted) {
			t.Errorf("Local inspector must not show %q:\n%s", unwanted, inspector)
		}
	}
	if _, _, _, ok := m.inspectorActionRect(); ok {
		t.Fatal("Local inspector must have no action rect")
	}
}

// TestCalendarManagerPreviewPaneClickOpensCalendar verifies clicking anywhere
// inside the previewed edit form focuses it — the detail opens for the
// selected calendar's immutable ID — while a click past the pane's right edge
// does nothing.
func TestCalendarManagerPreviewPaneClickOpensCalendar(t *testing.T) {
	m := newFlatManager().selectCalendar(3)
	px, py, pw, ph, ok := m.previewPaneRect()
	if !ok {
		t.Fatal("calendar selection has no preview pane rect")
	}
	clicked, cmd := m.Update(tea.MouseClickMsg{X: px + pw/2, Y: py + ph/2, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("preview click emitted a command %T", cmd())
	}
	if clicked.Screen() != CalendarManagerScreenCalendar || clicked.calendarForm == nil {
		t.Fatalf("preview click did not open calendar detail: screen=%v form=%v", clicked.Screen(), clicked.calendarForm)
	}
	if got := clicked.calendarForm.Draft().ID; got != 3 {
		t.Fatalf("preview click opened calendar %d, want immutable ID 3", got)
	}
	// A click one cell past the pane's right edge must not open the detail.
	missed, _ := m.Update(tea.MouseClickMsg{X: px + pw, Y: py, Button: tea.MouseLeft})
	if missed.calendarForm != nil {
		t.Fatal("click past the preview pane's right edge opened the calendar detail")
	}
}

// TestCalendarManagerInspectorAccountActionClickEmitsTarget verifies clicking
// the account inspector's Account Settings… button emits a typed account
// target for the selected account and leaves the root list mounted.
func TestCalendarManagerInspectorAccountActionClickEmitsTarget(t *testing.T) {
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	ax, ay, _, ok := m.inspectorActionRect()
	if !ok {
		t.Fatal("account inspector has no action rect")
	}
	clicked, cmd := m.Update(tea.MouseClickMsg{X: ax, Y: ay, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("Account Settings… click emitted no command")
	}
	msg, ok := cmd().(CalendarManagerRequestedMsg)
	if !ok {
		t.Fatalf("expected CalendarManagerRequestedMsg, got %T", cmd())
	}
	if msg.Target != CalendarManagerTargetAccount || msg.AccountID != 7 {
		t.Fatalf("account action msg = %+v, want {Target:Account AccountID:7}", msg)
	}
	if clicked.Screen() != CalendarManagerScreenList {
		t.Fatalf("account action should not push a screen: screen=%v", clicked.Screen())
	}
}

// TestCalendarManagerInspectorPadsExactlyHeight verifies the selection
// inspector composes exactly its height in rows for both the calendar
// edit-form preview and the account summary with its pinned action, so layout
// never leaves a ragged bottom or pushes content off.
func TestCalendarManagerInspectorPadsExactlyHeight(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	w, h := m.inspectorPaneSize()
	lines := m.selectionInspectorLines(w, h)
	if len(lines) != h {
		t.Fatalf("calendar preview lines = %d, want exactly %d", len(lines), h)
	}
	if !strings.Contains(stripANSI(strings.Join(lines, "\n")), "Save") {
		t.Fatal("calendar preview did not render the form's button row")
	}

	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	lines = m.selectionInspectorLines(w, h)
	if len(lines) != h {
		t.Fatalf("account inspector lines = %d, want exactly %d", len(lines), h)
	}
	if !strings.Contains(stripANSI(lines[len(lines)-1]), "Account Settings…") {
		t.Fatalf("last account inspector row is not the pinned action: %q", lines[len(lines)-1])
	}
}

// TestCalendarManagerInspectorLongDescriptionKeepsPreviewHeight verifies a
// long description cannot distort the preview: the form's Description field
// truncates it, the pane still composes exactly its height, and entering the
// editor keeps working.
func TestCalendarManagerInspectorLongDescriptionKeepsPreviewHeight(t *testing.T) {
	cals := flatManagerCalendars()
	base := cals[3]
	base.Description = strings.Repeat("A long calendar description word. ", 80)
	cals[3] = base
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(120, 40).selectCalendar(3)

	w, h := m.inspectorPaneSize()
	lines := m.selectionInspectorLines(w, h)
	if len(lines) != h {
		t.Fatalf("inspector lines = %d, want exactly %d (description overflowed)", len(lines), h)
	}

	opened, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if opened.calendarForm == nil || opened.calendarForm.Draft().ID != 3 {
		t.Fatal("editor not reachable with a long description present")
	}
}

func TestCalendarManagerSelectsFirstCalendarWhenInitialDataLoads(t *testing.T) {
	m := NewCalendarManagerModel(nil, nil, help.New()).SetSize(120, 40)
	m = m.SetData(flatManagerCalendars(), nil)
	if id, ok := m.selectedID(); !ok || id != 1 {
		t.Fatalf("initial loaded selection = %d ok=%v, want first calendar 1", id, ok)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Edit calendar") {
		t.Fatalf("initial calendar edit-form preview did not render:\n%s", view)
	}
}

func TestCalendarManagerWideCalendarEditorKeepsHierarchyMounted(t *testing.T) {
	m := newFlatManager().selectCalendar(1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view := stripANSI(m.View())
	if !strings.Contains(view, "Local") || !strings.Contains(view, "Primary") {
		t.Fatalf("wide calendar editor did not keep hierarchy mounted:\n%s", view)
	}
	if !strings.Contains(view, "Name") || !strings.Contains(view, "Save") {
		t.Fatalf("wide calendar editor missing inline form:\n%s", view)
	}
}

func TestCalendarManagerWideAccountInspectorKeepsHierarchyMounted(t *testing.T) {
	m := newFlatManager().selectCalendar(2).OpenAccount(AccountSettingsParams{
		AccountID: 7, DisplayName: "Google", Provider: "google", CalendarCount: 2,
	})
	view := stripANSI(m.View())
	if !strings.Contains(view, "Local") || !strings.Contains(view, "Primary") {
		t.Fatalf("wide account inspector did not keep hierarchy mounted:\n%s", view)
	}
	if !strings.Contains(view, "Manage Calendars") || !strings.Contains(view, "Rename Account") {
		t.Fatalf("wide account inspector missing inline actions:\n%s", view)
	}
}

func TestCalendarManagerWideImportKeepsHierarchyMounted(t *testing.T) {
	m := newFlatManager().OpenImport(41)
	view := stripANSI(m.View())
	if !strings.Contains(view, "Local") || !strings.Contains(view, "Primary") {
		t.Fatalf("wide import did not keep hierarchy mounted:\n%s", view)
	}
	if !strings.Contains(view, "Import iCal file") || !strings.Contains(view, "Path") {
		t.Fatalf("wide import missing inline form:\n%s", view)
	}
}

func TestCalendarManagerShallowWideFallbackSizesWholeListPane(t *testing.T) {
	m := newFlatManager().SetSize(100, 10)
	boxW, _ := m.BoxSize()
	wantW := max(boxW-5, 10)
	listW, _ := m.rootPaneSize()
	_, _, hitW, _ := m.listRegion()
	if listW != wantW || hitW != wantW {
		t.Fatalf("one-pane list widths: size=%d hit=%d want=%d", listW, hitW, wantW)
	}
	if got := m.list.width; got != wantW {
		t.Fatalf("sized list width = %d, want %d", got, wantW)
	}
	// Width permits the menu's natural content, so the longest label must not
	// be needlessly truncated (regression: the cap once bound the box height
	// instead of its width, truncating labels even on a wide terminal).
	mm := m.openAddMenu()
	longest := 0
	for _, item := range calendarManagerAddItems {
		longest = max(longest, lipgloss.Width(item.label))
	}
	natural := longest + 1 + calendarManagerMenuTrailing
	if got := mm.addMenuContentWidth(); got != natural {
		t.Fatalf("menu content width = %d, want natural %d (label truncated at wide box)", got, natural)
	}
}

func TestCalendarManagerNarrowInspectorUsesOnePaneAndBackRestoresList(t *testing.T) {
	m := newFlatManager().SetSize(narrowThreshold-1, 30).selectCalendar(1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view := stripANSI(m.View())
	if strings.Contains(view, "+ Add") || !strings.Contains(view, "Name") {
		t.Fatalf("narrow editor should show only inspector pane:\n%s", view)
	}
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc did not emit inspector close")
	}
	m, _ = m.Update(cmd())
	if m.Screen() != CalendarManagerScreenList || !strings.Contains(stripANSI(m.View()), "Local") {
		t.Fatalf("Back did not restore narrow hierarchy: screen=%v\n%s", m.Screen(), stripANSI(m.View()))
	}
}

func TestCalendarManagerBoxSizeStaysOnManagerShell(t *testing.T) {
	root := newFlatManager()
	wantW, wantH := root.BoxSize()
	states := []CalendarManagerModel{
		root.OpenCalendar(calendarDialogParamsFor(1, root.calendars[1], false)),
		root.OpenAccount(AccountSettingsParams{AccountID: 7, DisplayName: "Google"}),
		root.OpenImport(9),
	}
	for _, state := range states {
		if gotW, gotH := state.BoxSize(); gotW != wantW || gotH != wantH {
			t.Fatalf("child changed manager shell size: got %dx%d want %dx%d", gotW, gotH, wantW, wantH)
		}
	}
}

// managerTabKey builds the manager root's Tab/Shift-Tab navigation key press.
func managerTabKey(shift bool) tea.KeyPressMsg {
	k := tea.KeyPressMsg{Code: tea.KeyTab}
	if shift {
		k.Mod = tea.ModShift
	}
	return k
}

// TestCalendarManagerRootFocusCyclesWideCalendar verifies the wide two-pane
// root Tab cycle visits every focusable control in order — list → + Add →
// inspector action → list — and Shift-Tab reverses it. Root focus never moves
// the list cursor, and the list only renders focused while it holds root focus.
// TestCalendarManagerRootFocusCyclesWideAccount verifies the full root ring
// with a remote account selected: list → + Add → inspector pill → list, both
// directions, without moving the selection.
func TestCalendarManagerRootFocusCyclesWideAccount(t *testing.T) {
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7}) // Google
	if _, _, _, ok := m.inspectorActionRect(); !ok {
		t.Fatal("precondition: wide account root must have a focusable inspector action")
	}

	// Forward cycle: list → add → inspector → list.
	for i, want := range []calendarManagerRootFocus{rootFocusAdd, rootFocusInspector, rootFocusList} {
		next, _ := m.Update(managerTabKey(false))
		m = next
		if m.rootFocus != want {
			t.Fatalf("forward tab step %d: focus=%v want %v", i, m.rootFocus, want)
		}
	}

	// Reverse cycle: list → inspector → add → list.
	for i, want := range []calendarManagerRootFocus{rootFocusInspector, rootFocusAdd, rootFocusList} {
		next, _ := m.Update(managerTabKey(true))
		m = next
		if m.rootFocus != want {
			t.Fatalf("reverse tab step %d: focus=%v want %v", i, m.rootFocus, want)
		}
	}

	// The selection cursor is independent of root focus and survives a cycle.
	if identity, ok := m.list.currentIdentity(); !ok || identity.id != 7 {
		t.Fatalf("root focus cycling moved the selection: got %+v ok=%v", identity, ok)
	}
	// Returning to the list re-focuses it; tabbing away blurs it.
	if !m.list.Focused() {
		t.Fatal("list not focused after cycling back to list root focus")
	}
	away, _ := m.Update(managerTabKey(false))
	if away.list.Focused() {
		t.Fatal("list should not render focused while + Add holds root focus")
	}
}

// TestCalendarManagerRootFocusTabEntersCalendarEditor verifies that with a
// calendar selected, Tab flows into the previewed edit form like any other
// control: forward Tab reaches it after + Add, reverse Shift-Tab reaches it
// directly, and both open the editor with list focus restored for Back.
func TestCalendarManagerRootFocusTabEntersCalendarEditor(t *testing.T) {
	// Forward: list → add → editor.
	m := newFlatManager().selectCalendar(3)
	m, _ = m.Update(managerTabKey(false))
	if m.rootFocus != rootFocusAdd || m.Screen() != CalendarManagerScreenList {
		t.Fatalf("first tab: focus=%v screen=%v, want add at root", m.rootFocus, m.Screen())
	}
	m, cmd := m.Update(managerTabKey(false))
	if cmd != nil {
		t.Fatalf("entering the editor emitted a command %T", cmd())
	}
	if m.Screen() != CalendarManagerScreenCalendar || m.calendarForm == nil {
		t.Fatalf("second tab did not enter the previewed editor: screen=%v", m.Screen())
	}
	if got := m.calendarForm.Draft().ID; got != 3 {
		t.Fatalf("tab entered calendar %d, want immutable ID 3", got)
	}
	if m.rootFocus != rootFocusList {
		t.Fatalf("root focus = %v after entering editor, want list for Back", m.rootFocus)
	}

	// Reverse: Shift-Tab from the list wraps straight into the editor.
	m = newFlatManager().selectCalendar(3)
	m, _ = m.Update(managerTabKey(true))
	if m.Screen() != CalendarManagerScreenCalendar || m.calendarForm == nil || m.calendarForm.Draft().ID != 3 {
		t.Fatalf("shift-tab did not enter the previewed editor: screen=%v", m.Screen())
	}
}

// TestCalendarManagerRootFocusCycleOmitsUnavailableInspector verifies that a
// narrow one-pane root and a wide root whose selection has no inspector action
// both omit the inspector from the cycle, so Tab bounces list ↔ + Add only.
func TestCalendarManagerRootFocusCycleOmitsUnavailableInspector(t *testing.T) {
	cases := []struct {
		name string
		m    CalendarManagerModel
	}{
		{"narrow one-pane", newFlatManager().SetSize(narrowThreshold-1, 30).selectCalendar(3)},
		{"wide local header", func() CalendarManagerModel {
			m := newFlatManager()
			m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 0})
			return m
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, ok := tc.m.inspectorActionRect(); ok {
				t.Fatal("precondition: inspector action must be unavailable")
			}
			m := tc.m
			// Forward: list → add → list (no inspector step).
			m, _ = m.Update(managerTabKey(false))
			if m.rootFocus != rootFocusAdd {
				t.Fatalf("tab list→add: focus=%v", m.rootFocus)
			}
			m, _ = m.Update(managerTabKey(false))
			if m.rootFocus != rootFocusList {
				t.Fatalf("tab add→list (inspector omitted): focus=%v", m.rootFocus)
			}
			// Reverse: list → add → list.
			m, _ = m.Update(managerTabKey(true))
			if m.rootFocus != rootFocusAdd {
				t.Fatalf("shift+tab list→add: focus=%v", m.rootFocus)
			}
		})
	}
}

// TestCalendarManagerRootFocusAddActivateOpensMenu verifies Enter and Space
// both activate the focused + Add action (open the menu) and emit no command.
func TestCalendarManagerRootFocusAddActivateOpensMenu(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEnter},
		{Code: ' ', Text: " "},
	} {
		m := newFlatManager()
		m, _ = m.Update(managerTabKey(false)) // list → add
		if m.rootFocus != rootFocusAdd {
			t.Fatalf("precondition: focus=%v want add", m.rootFocus)
		}
		activated, cmd := m.Update(key)
		if cmd != nil {
			t.Fatalf("key %s: activation emitted command %T", key.String(), cmd())
		}
		if !activated.addMenuOpen {
			t.Fatalf("key %s did not open the add menu while + Add is focused", key.String())
		}
	}
}

// TestCalendarManagerRootFocusInspectorActivateOpensCalendar verifies Enter
// and Space both activate the focused inspector action, opening the selected
// calendar's detail by immutable ID (unchanged routing).
// TestCalendarManagerRootFocusInspectorActivateEmitsAccountTarget verifies
// Enter and Space on the focused account pill emit the typed account target,
// mirroring the mouse path.
func TestCalendarManagerRootFocusInspectorActivateEmitsAccountTarget(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEnter},
		{Code: ' ', Text: " "},
	} {
		m := newFlatManager()
		m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
		m, _ = m.Update(managerTabKey(false)) // list → add
		m, _ = m.Update(managerTabKey(false)) // add → inspector
		if m.rootFocus != rootFocusInspector {
			t.Fatalf("precondition: focus=%v want inspector", m.rootFocus)
		}
		activated, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("key %s: pill activation emitted no command", key.String())
		}
		msg, ok := cmd().(CalendarManagerRequestedMsg)
		if !ok || msg.Target != CalendarManagerTargetAccount || msg.AccountID != 7 {
			t.Fatalf("key %s emitted %T/%+v, want account target 7", key.String(), cmd(), msg)
		}
		if activated.Screen() != CalendarManagerScreenList {
			t.Fatalf("key %s pushed a screen: %v", key.String(), activated.Screen())
		}
	}
}

// TestCalendarManagerRootFocusListRetainsArrowsSpace verifies that while the
// list holds root focus, arrows navigate, Space toggles visibility, and Enter
// opens the selected calendar — and that arrows no longer move the cursor once
// another control holds root focus.
func TestCalendarManagerRootFocusListRetainsArrowsSpace(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, visible
	// Down moves the selection to the next calendar (3).
	moved, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if id, _ := moved.selectedID(); id != 3 {
		t.Fatalf("down arrow did not move selection: got %d want 3", id)
	}
	// Space toggles visibility of the selected calendar.
	_, cmd := moved.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("space did not emit a visibility toggle while list-focused")
	}
	if _, ok := cmd().(CalendarVisibilityToggledMsg); !ok {
		t.Fatalf("space emitted %T, want CalendarVisibilityToggledMsg", cmd())
	}
	// Enter opens the selected calendar's detail internally.
	opened, cmd := moved.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || opened.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("enter did not open calendar detail while list-focused: cmd=%v screen=%v", cmd, opened.Screen())
	}

	// Once + Add holds root focus, arrows must not move the list cursor.
	m = newFlatManager().selectCalendar(2)
	m, _ = m.Update(managerTabKey(false)) // → add
	before, _ := m.selectedID()
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	after, _ := m.selectedID()
	if before != after {
		t.Fatalf("down arrow moved selection while + Add focused: %d → %d", before, after)
	}
}

// TestCalendarManagerRootFocusMouseRestoresFocus verifies that mouse clicks on
// the source list, + Add action, and inspector action each restore the matching
// root focus before routing.
func TestCalendarManagerRootFocusMouseRestoresFocus(t *testing.T) {
	t.Run("list row click focuses list", func(t *testing.T) {
		m := newFlatManager().selectCalendar(2)
		m, _ = m.Update(managerTabKey(false)) // move focus away → add
		listX, listY, _, _ := m.listRegion()
		row := calendarListRowForCalendarID(t, m.list, 3) - m.list.offset
		clicked, _ := m.Update(tea.MouseClickMsg{X: listX + 8, Y: listY + row, Button: tea.MouseLeft})
		if clicked.rootFocus != rootFocusList {
			t.Fatalf("list click focus=%v want list", clicked.rootFocus)
		}
		if !clicked.list.Focused() {
			t.Fatal("list not focused after list click")
		}
	})

	t.Run("add action click focuses add", func(t *testing.T) {
		m := newFlatManager()
		ax, ay, _, ok := m.sourceAddActionRect()
		if !ok {
			t.Fatal("no + Add action rect")
		}
		clicked, _ := m.Update(tea.MouseClickMsg{X: ax, Y: ay, Button: tea.MouseLeft})
		if clicked.rootFocus != rootFocusAdd {
			t.Fatalf("add click focus=%v want add", clicked.rootFocus)
		}
		if !clicked.addMenuOpen {
			t.Fatal("add click did not open the menu")
		}
	})

	t.Run("inspector action click focuses inspector", func(t *testing.T) {
		m := newFlatManager()
		m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7}) // Google account action
		ax, ay, _, ok := m.inspectorActionRect()
		if !ok {
			t.Fatal("no inspector action rect")
		}
		clicked, cmd := m.Update(tea.MouseClickMsg{X: ax, Y: ay, Button: tea.MouseLeft})
		if cmd == nil {
			t.Fatal("inspector click emitted no command")
		}
		if clicked.rootFocus != rootFocusInspector {
			t.Fatalf("inspector click focus=%v want inspector", clicked.rootFocus)
		}
		if clicked.Screen() != CalendarManagerScreenList {
			t.Fatalf("account action should stay on root: screen=%v", clicked.Screen())
		}
	})
}

// TestCalendarManagerRootFocusNormalizesAfterResizeToOnePane verifies that a
// resize which drops the inspector out of the layout (wide two-pane → narrow
// one-pane) also drops inspector root focus back to the list. Otherwise Enter
// or Space would still invoke the now-hidden inspector action — an invisible
// control driving input. After normalization Space toggles the selected
// calendar's visibility (the list behavior) instead of opening the detail.
func TestCalendarManagerRootFocusNormalizesAfterResizeToOnePane(t *testing.T) {
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7}) // Google pill
	// Wide two-pane: tab onto the inspector pill.
	m, _ = m.Update(managerTabKey(false)) // list → add
	m, _ = m.Update(managerTabKey(false)) // add → inspector
	if m.rootFocus != rootFocusInspector {
		t.Fatalf("precondition: focus=%v want inspector", m.rootFocus)
	}

	// Resize into one-pane layout: the inspector pane is no longer rendered.
	m = m.SetSize(narrowThreshold-1, 30)
	if m.rootFocus != rootFocusList {
		t.Fatalf("resize to one-pane left focus=%v, want list", m.rootFocus)
	}
	if !m.list.Focused() {
		t.Fatal("list not focused after resize normalized root focus to list")
	}

	// Space must act on the list (toggle the selected calendar), never invoke
	// the hidden inspector target. Stale inspector focus on a calendar
	// selection must normalize away on resize too.
	m = newFlatManager().selectCalendar(3).setRootFocus(rootFocusInspector)
	m = m.SetSize(narrowThreshold-1, 30)
	if m.rootFocus != rootFocusList {
		t.Fatalf("resize with calendar preview focus left focus=%v, want list", m.rootFocus)
	}
	toggled, cmd := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if toggled.Screen() != CalendarManagerScreenList {
		t.Fatalf("space invoked a hidden inspector target: screen=%v", toggled.Screen())
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok || msg.ID != 3 {
		t.Fatalf("space after resize = %T/%+v, want CalendarVisibilityToggledMsg{ID:3}", cmd(), msg)
	}
}

// TestCalendarManagerRootFocusAddNotFocusedOnPushedScreen verifies that when
// root focus is on + Add and a pushed screen opens (where the + Add action is
// muted and inert), the action never renders as a focused pill: a disabled
// control must not carry the focus ring. Focus persists for the return-to-root
// case, but the focused styling is gated on the action being active.
func TestCalendarManagerRootFocusAddNotFocusedOnPushedScreen(t *testing.T) {
	m := newFlatManager()
	m, _ = m.Update(managerTabKey(false)) // list → add
	if m.rootFocus != rootFocusAdd {
		t.Fatalf("precondition: focus=%v want add", m.rootFocus)
	}
	// A pushed screen opens; the + Add action is muted/inert there.
	m = m.OpenCalendar(calendarDialogParamsFor(1, m.calendars[1], false))
	if m.sourceAddActionActive() {
		t.Fatal("precondition: + Add must be inactive on a pushed screen")
	}
	if m.rootFocus != rootFocusAdd {
		t.Fatalf("precondition: root focus should persist across the push, got %v", m.rootFocus)
	}
	// The persisted focus must not render a focused pill on the inert action.
	want := lipgloss.NewStyle().Faint(true).Render("+ Add")
	if got := m.renderSourceAddActionCore(); got != want {
		t.Fatalf("inactive + Add rendered as focused/active while rootFocus=Add on a pushed screen:\n got=%q\nwant=%q", stripANSI(got), stripANSI(want))
	}
	// Back at the root the action is active again, so focus re-asserts.
	back := m.CloseDetail()
	if back.sourceAddActionActive() && back.rootFocus == rootFocusAdd {
		focused := DefaultButtonStyles().Normal.Render("+ Add", true)
		if got := back.renderSourceAddActionCore(); got != focused {
			t.Fatalf("active + Add did not render focused at root after returning from pushed screen:\n got=%q\nwant=%q", stripANSI(got), stripANSI(focused))
		}
	} else {
		t.Fatalf("return-to-root state: active=%v focus=%v", back.sourceAddActionActive(), back.rootFocus)
	}
}

// TestCalendarManagerRootFocusKeepsSelectionVisibleInactive verifies that
// moving root focus off the list (to + Add) keeps the selected row visibly
// highlighted with the neutral inactive style, restores the active accent when
// focus returns, and never moves the selection cursor. It covers both a
// calendar row and a selectable account header, since the two render through
// separate paths (renderCalendarRow vs renderAccountHeader).
func TestCalendarManagerRootFocusKeepsSelectionVisibleInactive(t *testing.T) {
	// Pin a theme with distinct accent (#112233) and button (#6c5ce7) colors
	// so the active and inactive highlights are independently detectable.
	theme := NewTheme(true)
	theme.Selected = lipgloss.Color("#112233")
	theme.SelectedText = lipgloss.Color("#ffffff")
	theme.ButtonBg = lipgloss.Color("#6c5ce7")

	m := newFlatManager().SetTheme(theme).selectCalendar(3) // Holidays: wide root previews its edit form
	if !m.inspectorFocusAvailable() {
		t.Fatal("precondition: wide calendar root must have a focusable inspector pane")
	}

	// While the list holds root focus the selected calendar row uses the active accent.
	if out := m.list.View(); !strings.Contains(out, "48;2;17;34;51") {
		t.Fatalf("focused selection must use active accent #112233: %q", out)
	}

	// Tab away to + Add: the list blurs but the selected row stays visible
	// with the neutral inactive background, and the selection is untouched.
	away, _ := m.Update(managerTabKey(false))
	if away.rootFocus != rootFocusAdd {
		t.Fatalf("precondition: focus=%v want add", away.rootFocus)
	}
	if away.list.Focused() {
		t.Fatal("list must be blurred once root focus moves to + Add")
	}
	if out := away.list.View(); strings.Contains(out, "48;2;17;34;51") {
		t.Fatalf("blurred selection must not use the active accent: %q", out)
	}
	if out := away.list.View(); !strings.Contains(out, "48;2;108;92;231") {
		t.Fatalf("blurred selection must use neutral inactive bg #6c5ce7: %q", out)
	}
	if id, ok := away.selectedID(); !ok || id != 3 {
		t.Fatalf("moving root focus changed the selection: got %d ok=%v", id, ok)
	}

	// Returning root focus to the list restores the active accent.
	back, _ := away.Update(managerTabKey(true)) // add → list (reverse ring)
	if back.rootFocus != rootFocusList {
		t.Fatalf("focus=%v want list", back.rootFocus)
	}
	if out := back.list.View(); !strings.Contains(out, "48;2;17;34;51") {
		t.Fatalf("restored focus must use the active accent: %q", out)
	}

	// Account headers render through a separate path; cover them too. A
	// focused account header carries no background, so the only visible signal
	// while blurred is the neutral inactive highlight.
	header := newFlatManager().SetTheme(theme)
	header.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7}) // Google
	header = header.applyRootFocus()                                               // list focused, cursor on header
	if out := header.list.View(); strings.Contains(out, "48;2;108;92;231") {
		t.Fatalf("focused account header must not paint the inactive background: %q", out)
	}
	headerAway, _ := header.Update(managerTabKey(false)) // list → add
	if headerAway.rootFocus != rootFocusAdd {
		t.Fatalf("header precondition: focus=%v want add", headerAway.rootFocus)
	}
	if out := headerAway.list.View(); !strings.Contains(out, "48;2;108;92;231") {
		t.Fatalf("blurred account header must paint the neutral inactive bg #6c5ce7: %q", out)
	}
}
