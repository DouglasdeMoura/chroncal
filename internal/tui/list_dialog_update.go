package tui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// NewListDialogModel builds an empty shell. Callers call the Setters on the
// returned value before the render.
func NewListDialogModel(h help.Model) ListDialogModel {
	vp := viewport.New()
	vp.MouseWheelEnabled = true
	return ListDialogModel{
		keys:  defaultListDialogKeys(),
		help:  h,
		body:  vp,
		cache: &viewRenderCache{},
	}
}

// MoveUp/MoveDown advance to the next selectable row inside the list zone.
// They no-op when the list is empty or focus is elsewhere. Selection changes
// reset the detail viewport so the new row starts from the top.
func (m ListDialogModel) MoveUp() ListDialogModel {
	if m.focusZone != ListZoneList {
		return m
	}
	for idx := m.selected - 1; idx >= 0; idx-- {
		if !m.rowIsDisabled(idx) {
			m.selected = idx
			m.body.GotoTop()
			break
		}
	}
	return m
}

func (m ListDialogModel) MoveDown() ListDialogModel {
	if m.focusZone != ListZoneList {
		return m
	}
	for idx := m.selected + 1; idx < len(m.rows); idx++ {
		if !m.rowIsDisabled(idx) {
			m.selected = idx
			m.body.GotoTop()
			break
		}
	}
	return m
}

// CycleZone advances focus to the next (or previous) enabled control. Disabled
// action buttons remain visible but do not participate in the Tab order.
func (m ListDialogModel) CycleZone(forward bool) ListDialogModel {
	type focusStop struct {
		zone   ListDialogZone
		action int
	}
	stops := []focusStop{{zone: ListZoneList}}
	for idx, action := range m.actions {
		if !action.Disabled {
			stops = append(stops, focusStop{zone: ListZoneActions, action: idx})
		}
	}
	if m.titleAction != nil && !m.titleAction.Disabled {
		stops = append(stops, focusStop{zone: ListZoneTitleAction})
	}
	if len(stops) == 1 {
		m.focusZone = ListZoneList
		return m
	}

	current := 0
	for idx, stop := range stops {
		if stop.zone == m.focusZone && (stop.zone != ListZoneActions || stop.action == m.focusedAction) {
			current = idx
			break
		}
	}
	delta := 1
	if !forward {
		delta = -1
	}
	next := stops[(current+delta+len(stops))%len(stops)]
	m.focusZone = next.zone
	if next.zone == ListZoneActions {
		m.focusedAction = next.action
	}
	return m
}

// FocusAction focuses an enabled action-bar button.
func (m ListDialogModel) FocusAction(idx int) ListDialogModel {
	if idx < 0 || idx >= len(m.actions) || m.actions[idx].Disabled {
		return m
	}
	m.focusZone = ListZoneActions
	m.focusedAction = idx
	return m
}

// Activate fires the focused button (FireAction or FireTitleAction). With
// no button to fire, the focus does not change.
func (m ListDialogModel) Activate() (ListDialogModel, tea.Cmd) {
	switch m.focusZone {
	case ListZoneActions:
		return m.FireAction(m.focusedAction)
	case ListZoneTitleAction:
		return m.FireTitleAction()
	case ListZoneList, ListZoneCustom:
	}
	return m, nil
}

// ActivateByKey is Activate for the Enter binding (Enter or Space). Space
// does not fire a danger button: a destructive action fires only by Enter,
// a click, or its own key.
func (m ListDialogModel) ActivateByKey(msg tea.KeyPressMsg) (ListDialogModel, tea.Cmd) {
	if msg.String() == "space" && m.focusedDanger() {
		return m, nil
	}
	return m.Activate()
}

// focusedDanger reports whether the focused button is a danger button.
func (m ListDialogModel) focusedDanger() bool {
	switch m.focusZone {
	case ListZoneActions:
		return m.focusedAction >= 0 && m.focusedAction < len(m.actions) && m.actions[m.focusedAction].Danger
	case ListZoneTitleAction:
		return m.titleAction != nil && m.titleAction.Danger
	case ListZoneList, ListZoneCustom:
	}
	return false
}

// FireAction returns the command of the action-bar button idx and puts the
// focus on the list. Every path that fires a button uses it: a hotkey,
// Enter or Space, and a click. An action can end with a prompt that the
// user cancels, or with a refusal. The list keys must then work, so no
// button keeps the focus after it fires (issue #909). An index out of
// range, a disabled button, or a button with no command fires nothing and
// keeps the focus.
func (m ListDialogModel) FireAction(idx int) (ListDialogModel, tea.Cmd) {
	if idx < 0 || idx >= len(m.actions) || m.actions[idx].Disabled || m.actions[idx].Msg == nil {
		return m, nil
	}
	m.focusZone = ListZoneList
	return m, m.actions[idx].Msg
}

// FireTitleAction is FireAction for the button of the title line.
func (m ListDialogModel) FireTitleAction() (ListDialogModel, tea.Cmd) {
	if m.titleAction == nil || m.titleAction.Disabled || m.titleAction.Msg == nil {
		return m, nil
	}
	m.focusZone = ListZoneList
	return m, m.titleAction.Msg
}

// RowAtPosition hit-tests a screen-space (x, y) against the rendered list.
// Returns the row index when the click lands on a row, false otherwise.
func (m ListDialogModel) RowAtPosition(x, y int) (int, bool) {
	if len(m.rows) == 0 || m.width <= 0 || m.height <= 0 {
		return 0, false
	}

	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	listX := dialogX + 2
	listY := dialogY + 4
	listW := innerW
	listH := bodyH

	if m.isNarrow() {
		listH = min(max(len(m.rows)+1, 3), max(bodyH/3, 3))
	} else {
		listW = listColumnWidth(innerW)
	}

	if x < listX || x >= listX+listW || y < listY || y >= listY+listH {
		return 0, false
	}

	row := y - listY
	if len(m.rows) > listH && row == listH-1 {
		return 0, false
	}

	idx := m.scroll + row
	if idx < 0 || idx >= len(m.rows) || m.rowIsDisabled(idx) {
		return 0, false
	}
	return idx, true

}

// ActionAtPosition hit-tests the action bar. Returns the clicked button index.
// Each button's width is measured from its actual rendered output. That
// matches DefaultButtonStyles: Padding(0,2) + MarginRight(1). The hit regions
// then agree exactly with what the user sees. The join space of
// renderActions (see actionGap) adds to the advance between two buttons.
func (m ListDialogModel) ActionAtPosition(x, y int) (int, bool) {
	ox, oy := m.actionBarOrigin()
	if y != oy {
		return 0, false
	}
	bs := DefaultButtonStyles()
	gap := m.actionGap(m.actionsWidth())
	cx := ox
	for i, a := range m.actions {
		w := actionButtonWidth(bs, a)
		if x >= cx && x < cx+w {
			return i, true
		}
		cx += w + gap // the separator of renderActions
	}
	return 0, false
}

// ClickRow selects idx and focuses the list zone. Resets the detail viewport
// scroll on selection change so the freshly clicked row's content starts at
// the top.
func (m ListDialogModel) ClickRow(idx int) ListDialogModel {
	if idx < 0 || idx >= len(m.rows) || m.rowIsDisabled(idx) {
		return m
	}
	if idx != m.selected {
		m.body.GotoTop()
	}
	m.selected = idx
	m.focusZone = ListZoneList
	return m
}

// DetailsOrigin returns the screen-space (x, y) of the first line of the
// detail pane. Callers can then hit-test buttons they composed into the
// detail lines (for example RSVP buttons in the event dialog).
func (m ListDialogModel) DetailsOrigin() (int, int) {
	boxW, boxH := m.boxSize()
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	detailsX := dialogX + 2
	detailsY := dialogY + 4
	if m.isNarrow() {
		rowCount := max(len(m.rows), 1)
		bodyH := max(max(boxH-3, 6)-4, 3)
		listH := min(max(rowCount+1, 3), max(bodyH/3, 3))
		detailsY += listH + 1
	} else {
		innerW := max(boxW-5, 10)
		detailsX += listColumnWidth(innerW) + dialogDividerWidth
	}
	return detailsX, detailsY
}

// BodyRowScreenY translates a content-row index inside the scrollable
// detail body to its screen-space Y. It includes the pinned title rows
// and their rule (when present) and the current scroll offset. Returns false
// when the row is scrolled out of view.
func (m ListDialogModel) BodyRowScreenY(idx int) (int, bool) {
	m.ensureLayout()
	_, oy := m.DetailsOrigin()
	if m.titleLines != nil {
		oy += len(m.titleLines) + 1
	}
	visible := idx - m.body.YOffset()
	if visible < 0 || visible >= m.body.Height() {
		return 0, false
	}
	return oy + visible, true
}

func (m ListDialogModel) actionBarOrigin() (int, int) {
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2

	contentX := dialogX + 2
	actionsY := dialogY + bodyH + 3

	if m.isNarrow() {
		return contentX, actionsY
	}
	return contentX + listColumnWidth(innerW) + dialogDividerWidth, actionsY
}

// TitleActionAtPosition reports whether (x, y) lies within the title-line
// action button. FireTitleAction fires the button.
func (m ListDialogModel) TitleActionAtPosition(x, y int) bool {
	if m.titleAction == nil || m.titleAction.Disabled || m.width <= 0 || m.height <= 0 {
		return false
	}
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	titleY := dialogY + 2
	if y != titleY {
		return false
	}
	btnW := lipgloss.Width(renderTitleActionButton(*m.titleAction, false))
	btnStartX := dialogX + 2 + innerW - btnW
	if x < btnStartX || x >= btnStartX+btnW {
		return false
	}
	return true
}

// HandleKey is the shell's handler for keys it cares about (navigation, tab,
// enter-on-actions, close). Returns the (maybe-updated) model and the
// result command. Callers dispatch their domain keys (New/Edit/Delete/…)
// themselves before they fall through to this.
func (m ListDialogModel) HandleKey(msg tea.KeyPressMsg, onClose func() tea.Msg) (ListDialogModel, tea.Cmd, bool) {
	m.ensureLayout()
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return onClose() }, true
	case key.Matches(msg, m.keys.Tab):
		return m.CycleZone(true), nil, true
	case key.Matches(msg, m.keys.ShiftTab):
		return m.CycleZone(false), nil, true
	case key.Matches(msg, m.keys.Up):
		return m.MoveUp(), nil, true
	case key.Matches(msg, m.keys.Down):
		return m.MoveDown(), nil, true
	case key.Matches(msg, m.keys.PageUp):
		m.body.PageUp()
		return m, nil, true
	case key.Matches(msg, m.keys.PageDown):
		m.body.PageDown()
		return m, nil, true
	case key.Matches(msg, m.keys.Home):
		m.body.GotoTop()
		return m, nil, true
	case key.Matches(msg, m.keys.End):
		m.body.GotoBottom()
		return m, nil, true
	case key.Matches(msg, m.keys.Enter):
		m, cmd := m.ActivateByKey(msg)
		return m, cmd, true
	}
	return m, nil, false
}

// HandleMouseWheel forwards mouse wheel events to the detail body. The
// user can then scroll long event content with the wheel. That is the same
// affordance the single-event dialog provides.
func (m ListDialogModel) HandleMouseWheel(msg tea.MouseWheelMsg) (ListDialogModel, tea.Cmd) {
	m.ensureLayout()
	var cmd tea.Cmd
	m.body, cmd = m.body.Update(msg)
	return m, cmd
}

// ScrollDetailsUp/Down nudge the detail viewport by one line. Callers use
// these when up/down arrows belong to the details pane (focus is on actions
// or RSVP, not on the list itself).
func (m ListDialogModel) ScrollDetailsUp() ListDialogModel {
	m.ensureLayout()
	m.body.ScrollUp(1)
	return m
}

func (m ListDialogModel) ScrollDetailsDown() ListDialogModel {
	m.ensureLayout()
	m.body.ScrollDown(1)
	return m
}
