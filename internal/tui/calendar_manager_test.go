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
// and a click on the source + Add action open the manager-local menu and emit
// no app command.
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
	})
}

// TestCalendarManagerAddMenuRowsAndNoCancel verifies the open menu renders the
// exact three rows and no Cancel affordance.
func TestCalendarManagerAddMenuRowsAndNoCancel(t *testing.T) {
	m := newFlatManager().openAddMenu()
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

// TestCalendarManagerAddMenuRowClickEmitsTarget verifies clicking each interior
// menu row emits the correct typed target and closes the menu.
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
	if got, want := strings.Join(labels, ","), "Set as Default,Export Calendar…,Delete Calendar…"; got != want {
		t.Fatalf("detail actions = %q, want %q", got, want)
	}
	for _, b := range buttons {
		msg := b.OnPress()
		switch msg := msg.(type) {
		case CalendarSetDefaultRequestedMsg:
			if msg.ID != 3 || msg.Name != "Holidays" {
				t.Errorf("Set Default = %+v, want ID 3", msg)
			}
		case CalendarExportRequestedMsg:
			if msg.ID != 3 || msg.Name != "Holidays" {
				t.Errorf("Export = %+v, want ID 3", msg)
			}
		case CalendarDeleteRequestedMsg:
			if msg.ID != 3 || msg.Name != "Holidays" {
				t.Errorf("Delete = %+v, want ID 3", msg)
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
	if row := managerCalendarLine(t, m1, 2); !strings.HasPrefix(row, Glyphs["checkbox.off"]+" ●") {
		t.Errorf("row did not flip to unchecked visibility: %q", row)
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
	if row := managerCalendarLine(t, m2, 2); !strings.HasPrefix(row, Glyphs["checkbox.on"]+" ●") {
		t.Errorf("row did not flip back to checked visibility: %q", row)
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
// detail renders "Location: Local" and has no Account opener (local
// calendars have no owning account to drill into).
func TestCalendarManagerDetailLocalHasLocationOnly(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, local
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	view := stripANSI(pushed.calendarForm.View())
	if !strings.Contains(view, "Location: Local") {
		t.Errorf("local detail missing Location: Local:\n%s", view)
	}
	if calendarDetailFieldIndex(pushed, "opener") >= 0 {
		t.Errorf("local detail should not expose an Account opener:\n%s", view)
	}
}

// TestCalendarManagerDetailRemoteHasAccountOpener verifies a remote calendar's
// detail renders an actionable "Account: <name> ›" opener that, when
// activated, pushes the owning account's settings onto the stack.
func TestCalendarManagerDetailRemoteHasAccountOpener(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google, account 7
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	view := stripANSI(pushed.calendarForm.View())
	if !strings.Contains(view, "Account: Google ›") {
		t.Errorf("remote detail missing actionable Account line:\n%s", view)
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
		"▾ Local", "▾ Google", "▾ Fastmail",
		Glyphs["checkbox.on"] + " ● Primary",
		"Location", "Google", "Enter  Edit Calendar",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("manager view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Edit ›") {
		t.Fatalf("calendar rows must not repeat Edit links beside the inspector:\n%s", view)
	}

	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	accountView := stripANSI(m.View())
	for _, want := range []string{"Google", "Calendars", "2", "Enter  Account Settings"} {
		if !strings.Contains(accountView, want) {
			t.Errorf("account inspector missing %q:\n%s", want, accountView)
		}
	}
}

func TestCalendarManagerSelectsFirstCalendarWhenInitialDataLoads(t *testing.T) {
	m := NewCalendarManagerModel(nil, nil, help.New()).SetSize(120, 40)
	m = m.SetData(flatManagerCalendars(), nil)
	if id, ok := m.selectedID(); !ok || id != 1 {
		t.Fatalf("initial loaded selection = %d ok=%v, want first calendar 1", id, ok)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Enter  Edit Calendar") {
		t.Fatalf("initial calendar inspector did not render:\n%s", view)
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
	if strings.Contains(view, "▾ Local") || !strings.Contains(view, "Name") {
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
