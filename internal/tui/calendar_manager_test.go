package tui

import (
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// flatManagerCalendars is a mixed Local + multi-account fixture. Its
// canonical order is Local first, then accounts by AccountOrder, then
// in-account DisplayOrder. That order is [1, 2, 3, 4]:
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

// calendarDetailFieldIndex returns the form item index of the first field of
// the given kind ("opener" or "checkbox") in the manager's active calendar
// detail. It returns -1 when no detail is open or no such field exists.
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
// first field of the given kind. Tests use this instead of Tab. The
// assertion is then independent of how many fields precede the target.
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
// the shared mouse tracker. It then resolves the Display Calendar checkbox zone
// into a terminal-space MouseClickMsg that hits it. Tests use it to exercise
// the mouse path of the visibility toggle with no hard-coded geometry.
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

type managerCommandProbeField struct {
	executed *bool
}

func (f *managerCommandProbeField) Update(tea.Msg) tea.Cmd {
	return func() tea.Msg {
		*f.executed = true
		return nil
	}
}

func (*managerCommandProbeField) View() string { return "" }

func (*managerCommandProbeField) Focus() tea.Cmd { return nil }

func (*managerCommandProbeField) Blur() {}

func (*managerCommandProbeField) SetWidth(int) {}

func (*managerCommandProbeField) IsFocusable() bool { return true }

// inspectorActionScreenRow maps the action's screen y into a View() row index.
// Tests can then read the rendered button without a re-derive of the box
// geometry.
func inspectorActionScreenRow(m CalendarManagerModel, ay int) int {
	_, boxH := m.boxSize()
	dialogY := (m.height - boxH) / 2
	return ay - dialogY
}

// managerTabKey builds the manager root's Tab/Shift-Tab navigation key press.
func managerTabKey(shift bool) tea.KeyPressMsg {
	k := tea.KeyPressMsg{Code: tea.KeyTab}
	if shift {
		k.Mod = tea.ModShift
	}
	return k
}

// TestCalendarManagerRootEnterPushesCalendarDetail verifies Enter pushes the
// selected calendar's detail onto the manager stack (OpenCalendar). It
// targets the immutable ID. It switches the screen. Root selection stays.
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

// TestCalendarManagerRootMouseRowOpensEdit verifies a click on the non-checkbox
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
	if toggled.Screen() != CalendarManagerScreenList || !toggled.list.IsHidden(2) {
		t.Fatalf("checkbox click opened edit or failed optimistic toggle: screen=%v hidden=%v", toggled.Screen(), toggled.list.IsHidden(2))
	}
}

// TestCalendarManagerAddActionLivesAtBottomOfSourceList verifies the header
// shows only "Calendars". The compact + Add action sits one row below the
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
// detail owns an unsaved draft. Add then cannot discard it in silence.
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
// action is muted and inert on every pushed screen, not just a calendar
// detail. Each pushed screen owns its own input. Covers the import
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
// emits CalendarReorderedMsg with the full canonical ID order. It swaps only
// within the same AccountID. It is a no-op across an owner boundary.
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
// value receiver as an alias of the parent's order slice via an in-place swap.
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
// calendar ID) across a data refresh. Edits and reloads then do not jump the
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
// manager. They emit CalendarManagerClosedMsg.
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
// and a click on the source + Add action open the manager-local menu. They emit
// no app command. They put root focus on the + Add action.
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
// menu cursor one row and wraps last→first. Shift-Tab reverses and wraps
// first→last. Neither key leaks into the root focus ring or the list
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
// selected menu row. It emits the same typed target as Enter. It closes the
// menu. It uses the shared activation binding rather than a
// menu-specific key code. Space and Enter then stay in lockstep.
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

// TestCalendarManagerAddMenuRowClickEmitsTarget verifies a click on each interior
// menu row emits the correct typed target. It closes the menu. It leaves root
// focus on + Add. After the host pushes a screen, return-to-root then lands
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
// a click outside the open menu dismisses it. It does not emit a command. It
// does not activate the list row underneath.
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
// closes the menu. It does not close the manager. It does not emit a command.
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
// is consumed. It neither activates a row nor dismisses the menu. It never
// routes to the list underneath.
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
// very narrow terminal the menu width is capped to the manager interior. The
// menu's natural width would overflow the box. The full rect then stays
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
// calendar detail exposes Export, Set Default, and Delete as lead actions.
// All of them carry the selected calendar's immutable ID. Delete is styled as
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
	if got, want := strings.Join(labels, ","), "Set as Default,Export Calendar…,Keep as Local Calendar…"; got != want {
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
	if got, want := strings.Join(labels, ","), "Set as Default,Export Calendar…,Move to Account…,Delete Calendar…"; got != want {
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
		case CalendarMoveToAccountRequestedMsg:
			if msg.ID != 1 || msg.Name != "On device" {
				t.Errorf("Move = %+v, want ID 1", msg)
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
	if m.list.IsHidden(2) {
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
	if !m1.list.IsHidden(2) {
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
	if m2.list.IsHidden(2) {
		t.Error("local hidden state not flipped back to false")
	}
	if row := managerCalendarLine(t, m2, 2); !strings.HasPrefix(row, "●") {
		t.Errorf("row did not flip back to the filled visibility circle: %q", row)
	}
}

// TestCalendarManagerListOwnsHiddenSet is the regression guard for issue #543.
// The calendar manager keeps a single source of truth for visibility: the
// embedded list. It does not keep a second mirrored map. After every toggle
// path the list-owned set, the reopened detail form, and a reload all agree.
// A reload replaces (never merges) the set. No stale or cleared ID can linger.
func TestCalendarManagerListOwnsHiddenSet(t *testing.T) {
	cals := flatManagerCalendars()
	initialHidden := map[int64]bool{3: true}
	m := NewCalendarManagerModel(cals, initialHidden, help.New()).
		SetSize(120, 40).selectCalendar(1)
	initialHidden[1] = true
	delete(initialHidden, 3)
	if hidden := m.list.HiddenSet(); hidden[1] || !hidden[3] {
		t.Fatalf("constructor retained caller hidden-map alias: %v", hidden)
	}

	// Open the detail so a forwarded CalendarVisibilityToggledMsg mirrors into
	// the list owner via updateCalendar (the path the app uses).
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("setup: detail form not open, screen=%v", m.Screen())
	}
	m, _ = m.Update(CalendarVisibilityToggledMsg{ID: 1, Hidden: true})
	if !m.list.IsHidden(1) {
		t.Fatal("forwarded detail toggle did not update the list-owned hidden set")
	}
	// The detail reopen path (inspector preview / openSelectedCalendar) reads
	// the same single owner, so it can never diverge from the row dot.
	if got := calendarDialogParamsFor(1, cals[1], m.list.IsHidden(1)).Hidden; !got {
		t.Fatalf("detail reopen reads hidden=%v while the list owns hidden=true", got)
	}

	// A reload replaces the whole set from the host map: no stale ID survives
	// and no previously-cleared ID lingers (the old two-map merge bug class).
	reloadedHidden := map[int64]bool{2: true}
	m = m.SetData(cals, reloadedHidden)
	reloadedHidden[1] = true
	delete(reloadedHidden, 2)
	if hidden := m.list.HiddenSet(); !hidden[2] || hidden[1] || hidden[3] {
		t.Fatalf("SetData did not clone and replace the owned hidden set: %v", hidden)
	}
}

// TestCalendarManagerRootSetDataClampsWhenSelectedTailRemoved verifies that
// a remove of the selected tail calendar leaves the cursor on a valid
// row that remains, instead of out of range.
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
