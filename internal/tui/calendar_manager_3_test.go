package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

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

// TestCalendarManagerRootFocusCyclesWideCalendar verifies the wide two-pane
// root Tab cycle visits every focusable control in order: list → + Add →
// inspector action → list. Shift-Tab reverses it. Root focus never moves
// the list cursor. The list only renders focused while it holds root focus.
// TestCalendarManagerRootFocusCyclesWideAccount verifies the full root ring
// with a remote account selected: list → + Add → inspector pill → list.
// Both directions. The selection does not move.
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
// control. Forward Tab reaches it after + Add. Reverse Shift-Tab reaches it
// directly. Both open the editor with list focus restored for Back.
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
// both omit the inspector from the cycle. Tab then bounces list ↔ + Add only.
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
// and Space both activate the focused inspector action. They open the selected
// calendar's detail by immutable ID (unchanged routing).
// TestCalendarManagerRootFocusInspectorActivateEmitsAccountTarget verifies
// Enter and Space on the focused account pill emit the typed account target.
// That matches the mouse path.
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
// opens the selected calendar. Arrows no longer move the cursor once
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
// the source list, + Add action, and inspector action each restore the right
// root focus before they route.
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

// TestCalendarManagerRootFocusNormalizesAfterResizeToOnePane verifies a resize
// that drops the inspector out of the layout. Wide two-pane becomes narrow
// one-pane. Inspector root focus then drops back to the list. Otherwise Enter
// or Space would still invoke the now-hidden inspector action. An invisible
// control would then drive input. After normalization Space toggles the
// selected calendar's visibility (the list behavior). It does not open the
// detail.
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

// TestCalendarManagerRootFocusAddNotFocusedOnPushedScreen verifies a pushed
// screen while root focus is on + Add. The + Add action is muted and inert
// there. The action never renders as a focused pill. A disabled
// control must not carry the focus ring. Focus persists for the return-to-root
// case. The focused style is gated on the action being active.
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

// TestCalendarManagerRootFocusKeepsSelectionVisibleInactive verifies a move of
// root focus off the list (to + Add). The selected row stays highlighted with
// the neutral inactive style. It restores the active accent when focus returns.
// It never moves the selection cursor. It covers a calendar row and a
// selectable account header. The two render through separate paths
// (renderCalendarRow vs renderAccountHeader).
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
	accent := backgroundSeq(theme.Selected)
	inactive := backgroundSeq(theme.ButtonBg)
	if out := m.list.View(); !strings.Contains(out, accent) {
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
	if out := away.list.View(); strings.Contains(out, accent) {
		t.Fatalf("blurred selection must not use the active accent: %q", out)
	}
	if out := away.list.View(); !strings.Contains(out, inactive) {
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
	if out := back.list.View(); !strings.Contains(out, accent) {
		t.Fatalf("restored focus must use the active accent: %q", out)
	}

	// Account headers render through a separate path; cover them too. A
	// focused account header carries no background, so the only visible signal
	// while blurred is the neutral inactive highlight.
	header := newFlatManager().SetTheme(theme)
	header.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7}) // Google
	header = header.applyRootFocus()                                               // list focused, cursor on header
	if out := header.list.View(); strings.Contains(out, inactive) {
		t.Fatalf("focused account header must not paint the inactive background: %q", out)
	}
	headerAway, _ := header.Update(managerTabKey(false)) // list → add
	if headerAway.rootFocus != rootFocusAdd {
		t.Fatalf("header precondition: focus=%v want add", headerAway.rootFocus)
	}
	if out := headerAway.list.View(); !strings.Contains(out, inactive) {
		t.Fatalf("blurred account header must paint the neutral inactive bg #6c5ce7: %q", out)
	}
}
