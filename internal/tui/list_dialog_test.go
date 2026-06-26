package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

func makeListDialogFixture() ListDialogModel {
	m := NewListDialogModel(newThemedHelp(NewTheme(false))).
		SetSize(120, 30).
		SetTitle("Calendars").
		SetRows([]string{"● Work", "● Personal", "● Family"}).
		SetDetailLines([]string{"Work", "", "Color  ● #a6e3a1"}).
		SetActions([]ListDialogAction{
			{Label: "New", Primary: true, Msg: func() tea.Msg { return "new" }},
			{Label: "Edit", Msg: func() tea.Msg { return "edit" }},
			{Label: "Delete", Danger: true, Msg: func() tea.Msg { return "delete" }},
		})
	return m
}

func TestListDialog_MoveDownAdvancesSelection(t *testing.T) {
	m := makeListDialogFixture()
	if got := m.Selected(); got != 0 {
		t.Fatalf("initial selection = %d, want 0", got)
	}
	m = m.MoveDown()
	if got := m.Selected(); got != 1 {
		t.Errorf("after MoveDown selection = %d, want 1", got)
	}
}

func TestListDialog_MoveUpClampsAtTop(t *testing.T) {
	m := makeListDialogFixture()
	m = m.MoveUp()
	if got := m.Selected(); got != 0 {
		t.Errorf("MoveUp at top = %d, want 0", got)
	}
}

func TestListDialog_MoveDownClampsAtBottom(t *testing.T) {
	m := makeListDialogFixture().SetSelected(2)
	m = m.MoveDown()
	if got := m.Selected(); got != 2 {
		t.Errorf("MoveDown at bottom = %d, want 2", got)
	}
}

func TestListDialog_TabCyclesThroughEveryAction(t *testing.T) {
	m := makeListDialogFixture()
	if got := m.FocusZone(); got != ListZoneList {
		t.Fatalf("initial focus = %v, want ListZoneList", got)
	}
	// Fixture has three actions, no title action, so Tab visits:
	// list → action[0] → action[1] → action[2] → list.
	for i := range 3 {
		m = m.CycleZone(true)
		if got := m.FocusZone(); got != ListZoneActions {
			t.Fatalf("CycleZone(true) #%d focus = %v, want ListZoneActions", i+1, got)
		}
		if got := m.focusedAction; got != i {
			t.Errorf("CycleZone(true) #%d focusedAction = %d, want %d", i+1, got, i)
		}
	}
	m = m.CycleZone(true)
	if got := m.FocusZone(); got != ListZoneList {
		t.Errorf("after wrapping focus = %v, want ListZoneList", got)
	}
}

func TestListDialog_TabIncludesTitleAction(t *testing.T) {
	action := ListDialogAction{Label: "New", Msg: func() tea.Msg { return "title" }}
	m := makeListDialogFixture().SetTitleAction(&action)

	// list → action[0] → action[1] → action[2] → title action → list
	m = m.CycleZone(true).CycleZone(true).CycleZone(true).CycleZone(true)
	if got := m.FocusZone(); got != ListZoneTitleAction {
		t.Fatalf("expected title action focus after 4 tabs, got %v", got)
	}
	cmd := m.ActivateFocused()
	if cmd == nil || cmd() != "title" {
		t.Errorf("ActivateFocused on title action did not return title-action msg")
	}
	m = m.CycleZone(true)
	if got := m.FocusZone(); got != ListZoneList {
		t.Errorf("after wrapping from title action focus = %v, want ListZoneList", got)
	}
}

func TestListDialog_ShiftTabReversesFromList(t *testing.T) {
	action := ListDialogAction{Label: "New", Msg: func() tea.Msg { return "title" }}
	m := makeListDialogFixture().SetTitleAction(&action)
	m = m.CycleZone(false)
	if got := m.FocusZone(); got != ListZoneTitleAction {
		t.Errorf("shift+tab from list should land on title action, got %v", got)
	}
}

func TestListDialog_ActivateFocusedReturnsActionMsg(t *testing.T) {
	m := makeListDialogFixture().FocusAction(1)
	cmd := m.ActivateFocused()
	if cmd == nil {
		t.Fatal("expected command from focused action")
	}
	if got := cmd(); got != "edit" {
		t.Errorf("cmd() = %v, want %q", got, "edit")
	}
}

func TestListDialog_ViewContainsTitleAndRows(t *testing.T) {
	m := makeListDialogFixture()
	out := m.View()
	for _, want := range []string{"Calendars", "Work", "Personal", "Family", "New", "Edit", "Delete"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing %q\n---\n%s\n---", want, out)
		}
	}
}

func TestListDialog_ViewHandlesEmptyList(t *testing.T) {
	m := NewListDialogModel(newThemedHelp(NewTheme(false))).
		SetSize(120, 30).
		SetTitle("Calendars").
		SetEmptyList("No calendars yet.", []string{"No calendars yet."})
	out := m.View()
	if !strings.Contains(out, "No calendars yet.") {
		t.Errorf("View() empty state missing message\n---\n%s\n---", out)
	}
}

func TestListDialog_RowAtPositionHitTestsList(t *testing.T) {
	m := makeListDialogFixture()
	boxW, boxH := m.BoxSize()
	dialogX := (120 - boxW) / 2
	dialogY := (30 - boxH) / 2
	idx, ok := m.RowAtPosition(dialogX+3, dialogY+4+1) // second row
	if !ok {
		t.Fatal("expected hit on second row")
	}
	if idx != 1 {
		t.Errorf("hit = %d, want 1", idx)
	}
}

func TestListDialog_CycleZoneSkipsActionsWhenEmpty(t *testing.T) {
	m := makeListDialogFixture().SetActions(nil)
	m = m.CycleZone(true)
	if got := m.FocusZone(); got != ListZoneList {
		t.Errorf("no actions: CycleZone should stay on list, got %v", got)
	}
}

// makeOverflowingListDialog returns a fixture whose detail pane has more
// lines than the available height, so scroll affordances must engage.
func makeOverflowingListDialog() ListDialogModel {
	long := make([]string, 80)
	for i := range long {
		long[i] = "detail line"
	}
	return NewListDialogModel(newThemedHelp(NewTheme(false))).
		SetSize(120, 24).
		SetTitle("Calendars").
		SetRows([]string{"● Work", "● Personal"}).
		SetDetailLines(long).
		SetActions([]ListDialogAction{
			{Label: "Edit", Msg: func() tea.Msg { return "edit" }},
			{Label: "Delete", Danger: true, Msg: func() tea.Msg { return "delete" }},
		})
}

func TestListDialog_OverflowSurfacesMoreHint(t *testing.T) {
	m := makeOverflowingListDialog()
	out := m.View()
	if !strings.Contains(out, "more") {
		t.Errorf("View() should advertise scrollable content via a more hint\n---\n%s\n---", out)
	}
}

func TestListDialog_PageDownScrollsDetailBody(t *testing.T) {
	m := makeOverflowingListDialog()
	// Render once so the viewport learns its dimensions and content size.
	_ = m.View()
	if m.body.AtBottom() {
		t.Fatalf("precondition: body should not start at bottom")
	}

	m, _, ok := m.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown}, func() tea.Msg { return nil })
	if !ok {
		t.Fatalf("HandleKey did not consume PgDn")
	}
	if m.body.YOffset() == 0 {
		t.Errorf("PgDn should move YOffset away from the top, got 0")
	}
}

func TestListDialog_MouseWheelScrollsDetailBody(t *testing.T) {
	m := makeOverflowingListDialog()
	_ = m.View()
	if got := m.body.YOffset(); got != 0 {
		t.Fatalf("precondition: YOffset = %d, want 0", got)
	}

	m, _ = m.HandleMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.body.YOffset() <= 0 {
		t.Errorf("wheel down should advance YOffset, got %d", m.body.YOffset())
	}
}

func TestListDialog_SelectionChangeResetsScroll(t *testing.T) {
	m := makeOverflowingListDialog()
	_ = m.View()
	m, _ = m.HandleMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.body.YOffset() == 0 {
		t.Fatalf("precondition: YOffset should be > 0 after wheel-down")
	}

	m = m.MoveDown()
	if got := m.body.YOffset(); got != 0 {
		t.Errorf("changing selection should reset scroll, got YOffset = %d", got)
	}
}

// TestListDialog_ActionAtPositionWidthMatchesRender verifies that the
// action-bar hit-test uses the actual rendered button width (lipgloss
// Padding+Margin) rather than len(label)+2.
//
// The bug: ActionAtPosition modelled each button as w=len(label)+2 and
// advanced cx by w+1.  DefaultButtonStyles uses Padding(0,2) (+4 cells)
// and MarginRight(1) (+1 cell), so the real per-button width is len+5.
// The undercount accumulates left-to-right, eventually routing a click
// inside a safe button's pill into the next (possibly destructive) button.
//
// Concrete failure (trash dialog, [Restore, Purge]):
//   - len("Restore")+2 = 9; buggy "Restore" range: [ox, ox+9)
//   - After buggy advance: "Purge" starts at ox+10
//   - Actual rendered "Restore" width = len+5 = 12; real range [ox, ox+12)
//   - Clicking at ox+10 is inside "Restore"'s pill, but the bug routes it
//     to "Purge" (the destructive button).
func TestListDialog_ActionAtPositionWidthMatchesRender(t *testing.T) {
	m := NewListDialogModel(newThemedHelp(NewTheme(false))).
		SetSize(120, 30).
		SetTitle("Trash").
		SetRows([]string{"deleted item"}).
		SetActions([]ListDialogAction{
			{Label: "Restore", Msg: func() tea.Msg { return "restore" }},
			{Label: "Purge", Danger: true, Msg: func() tea.Msg { return "purge" }},
		})

	ox, oy := m.actionBarOrigin()

	// ox+10 is inside "Restore"'s actual pill (real width=12) but past the
	// buggy boundary (len("Restore")+2=9).  With the bug the click routes
	// to "Purge"; after the fix it must route to "Restore" (index 0).
	clickX := ox + len("Restore") + 3 // = ox+10; within pill, past buggy width
	idx, ok := m.ActionAtPosition(clickX, oy)
	if !ok {
		t.Fatalf("ActionAtPosition(%d,%d): no match; click is inside Restore's pill", clickX, oy)
	}
	if idx != 0 {
		t.Errorf("ActionAtPosition(%d,%d) = %d, want 0 (Restore); "+
			"a click inside Restore's pill must not route to Purge (1)", clickX, oy, idx)
	}

	// Sanity-check: a click clearly inside "Purge" must still return 1.
	bs := DefaultButtonStyles()
	restoreW := lipgloss.Width(bs.Normal.Render("Restore", false))
	purgeStartX := ox + restoreW + 1 // +1 for the strings.Join(" ") separator
	purgeIdx, purgeOK := m.ActionAtPosition(purgeStartX+1, oy)
	if !purgeOK || purgeIdx != 1 {
		t.Errorf("ActionAtPosition(%d,%d) = (%d, %v), want (1, true): "+
			"click inside Purge's pill must route to Purge", purgeStartX+1, oy, purgeIdx, purgeOK)
	}
}
