package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// Focus-zone IDs used by currentZone and the tab-order helpers. Kept as
// ints so switch-based callers that already exist stay unchanged.
const (
	zoneList        = 0
	zoneRSVP        = 1
	zoneAction      = 2
	zoneTitleAction = 3
)

func (m EventDialogModel) currentZone() int {
	if m.rsvpFocused {
		return zoneRSVP
	}
	switch m.shell.FocusZone() {
	case ListZoneActions:
		return zoneAction
	case ListZoneTitleAction:
		return zoneTitleAction
	case ListZoneList, ListZoneCustom:
		return zoneList
	}
	return zoneList
}

// focusRSVP moves focus to the head of the RSVP button zone. Invoked by the
// RSVP hotkeys (y/n/m) and by a click of an RSVP button. Tab uses focusStop
// below to land on a specific element.
func (m EventDialogModel) focusRSVP() EventDialogModel {
	m.rsvpFocused = true
	m.shell = m.shell.SetFocusZone(ListZoneCustom)
	return m
}

// tabStop identifies one focusable control in the dialog. Tab/Shift+Tab
// walk every stop in order. Keyboard navigation then reaches each element
// the way a web page's tab order would. That is list → each RSVP button
// → each action button → title action.
type tabStop struct {
	kind int // 0=list, 1=rsvp button, 2=action button, 3=title action
	idx  int
}

func (m EventDialogModel) tabOrder() []tabStop {
	stops := []tabStop{{kind: zoneList}}
	for i := range m.rsvpActions() {
		stops = append(stops, tabStop{kind: zoneRSVP, idx: i})
	}
	for i := range m.actions() {
		stops = append(stops, tabStop{kind: zoneAction, idx: i})
	}
	if m.shell.HasTitleAction() {
		stops = append(stops, tabStop{kind: zoneTitleAction})
	}
	return stops
}

func (m EventDialogModel) currentStop() tabStop {
	if m.rsvpFocused {
		return tabStop{kind: zoneRSVP, idx: m.focusedRSVP}
	}
	switch m.shell.FocusZone() {
	case ListZoneActions:
		return tabStop{kind: zoneAction, idx: m.shell.FocusedAction()}
	case ListZoneTitleAction:
		return tabStop{kind: zoneTitleAction}
	case ListZoneList, ListZoneCustom:
		return tabStop{kind: zoneList}
	}
	return tabStop{kind: zoneList}
}

func (m EventDialogModel) focusStop(s tabStop) EventDialogModel {
	switch s.kind {
	case zoneList:
		m.rsvpFocused = false
		m.shell = m.shell.SetFocusZone(ListZoneList)
	case zoneRSVP:
		m.rsvpFocused = true
		m.focusedRSVP = s.idx
		m.shell = m.shell.SetFocusZone(ListZoneCustom)
	case zoneAction:
		m.rsvpFocused = false
		m.shell = m.shell.FocusAction(s.idx)
	case zoneTitleAction:
		m.rsvpFocused = false
		m.shell = m.shell.SetFocusZone(ListZoneTitleAction)
	}
	return m
}

func (m EventDialogModel) cycleZone(forward bool) EventDialogModel {
	order := m.tabOrder()
	if len(order) <= 1 {
		return m
	}
	cur := m.currentStop()
	idx := 0
	for i, s := range order {
		if s == cur {
			idx = i
			break
		}
	}
	delta := 1
	if !forward {
		delta = -1
	}
	return m.focusStop(order[(idx+delta+len(order))%len(order)])
}

func (m EventDialogModel) Update(msg tea.Msg) (EventDialogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	case tea.MouseWheelMsg:
		shell, cmd := m.shell.HandleMouseWheel(msg)
		m.shell = shell
		return m, cmd
	}
	return m, nil
}

func (m EventDialogModel) handleKey(msg tea.KeyPressMsg) (EventDialogModel, tea.Cmd) {
	sk := m.shell.Keys()
	rsvp := m.rsvpActions()
	acts := m.actions()

	switch {
	case key.Matches(msg, sk.Close):
		return m, func() tea.Msg { return EventDialogClosedMsg{} }

	case key.Matches(msg, sk.Tab):
		return m.cycleZone(true).refresh(), nil
	case key.Matches(msg, sk.ShiftTab):
		return m.cycleZone(false).refresh(), nil

	case key.Matches(msg, m.keys.Left):
		switch m.currentZone() {
		case zoneList:
			prev := m.day.AddDate(0, 0, -1)
			return m, func() tea.Msg { return DialogDayChangedMsg{Day: prev} }
		case zoneRSVP:
			if len(rsvp) > 0 {
				m.focusedRSVP = (m.focusedRSVP - 1 + len(rsvp)) % len(rsvp)
				return m.refresh(), nil
			}
		case zoneAction:
			if n := len(acts); n > 0 {
				m.shell = m.shell.FocusAction((m.shell.FocusedAction() - 1 + n) % n)
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Right):
		switch m.currentZone() {
		case zoneList:
			next := m.day.AddDate(0, 0, 1)
			return m, func() tea.Msg { return DialogDayChangedMsg{Day: next} }
		case zoneRSVP:
			if len(rsvp) > 0 {
				m.focusedRSVP = (m.focusedRSVP + 1) % len(rsvp)
				return m.refresh(), nil
			}
		case zoneAction:
			if n := len(acts); n > 0 {
				m.shell = m.shell.FocusAction((m.shell.FocusedAction() + 1) % n)
			}
		}
		return m, nil

	case key.Matches(msg, sk.Up):
		if m.currentZone() == zoneList {
			m.shell = m.shell.MoveUp()
			return m.refresh(), nil
		}
		m.shell = m.shell.ScrollDetailsUp()
		return m, nil

	case key.Matches(msg, sk.Down):
		if m.currentZone() == zoneList {
			m.shell = m.shell.MoveDown()
			return m.refresh(), nil
		}
		m.shell = m.shell.ScrollDetailsDown()
		return m, nil

	case key.Matches(msg, sk.Enter):
		switch m.currentZone() {
		case zoneList:
			if len(m.events) == 0 {
				day := m.day
				return m, func() tea.Msg { return EventCreateMsg{Day: day} }
			}
		case zoneRSVP:
			if m.focusedRSVP >= 0 && m.focusedRSVP < len(rsvp) {
				return m, rsvp[m.focusedRSVP].msg
			}
		case zoneAction, zoneTitleAction:
			return m, m.shell.ActivateFocused()
		}
		return m, nil

	case key.Matches(msg, m.keys.Edit):
		if _, ok := m.selectedEvent(); ok && len(acts) > 0 {
			m.shell = m.shell.FocusAction(0)
			m.rsvpFocused = false
			return m, acts[0].Msg
		}
	case key.Matches(msg, m.keys.Duplicate):
		if _, ok := m.selectedEvent(); ok && len(acts) > 1 {
			m.shell = m.shell.FocusAction(1)
			m.rsvpFocused = false
			return m, acts[1].Msg
		}
	case key.Matches(msg, m.keys.Delete):
		if _, ok := m.selectedEvent(); ok && len(acts) > 2 {
			m.shell = m.shell.FocusAction(2)
			m.rsvpFocused = false
			return m, acts[2].Msg
		}
	case key.Matches(msg, m.keys.Create):
		day := m.day
		return m, func() tea.Msg { return EventCreateMsg{Day: day} }
	case key.Matches(msg, m.keys.Copy):
		if ev, ok := m.selectedEvent(); ok {
			cal := m.calendars[ev.CalendarID]
			return m, copyEventDetailsCmd(formatEventDetailsText(ev, cal.Name))
		}
	case key.Matches(msg, m.keys.RSVPYes):
		if len(rsvp) > 0 {
			m = m.focusRSVP()
			m.focusedRSVP = 0
			return m.refresh(), rsvp[0].msg
		}
	case key.Matches(msg, m.keys.RSVPNo):
		if len(rsvp) > 1 {
			m = m.focusRSVP()
			m.focusedRSVP = 1
			return m.refresh(), rsvp[1].msg
		}
	case key.Matches(msg, m.keys.RSVPMaybe):
		if len(rsvp) > 2 {
			m = m.focusRSVP()
			m.focusedRSVP = 2
			return m.refresh(), rsvp[2].msg
		}
	}
	return m, nil
}

func (m EventDialogModel) handleMouse(msg tea.MouseClickMsg) (EventDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}

	if cmd, ok := m.shell.TitleActionAtPosition(msg.X, msg.Y); ok {
		return m, cmd
	}

	if len(m.events) == 0 {
		return m, nil
	}

	if idx, ok := m.shell.RowAtPosition(msg.X, msg.Y); ok {
		m.shell = m.shell.ClickRow(idx)
		m.rsvpFocused = false
		return m.refresh(), nil
	}
	if idx, ok := m.shell.ActionAtPosition(msg.X, msg.Y); ok {
		shell, cmd := m.shell.ClickAction(idx)
		m.shell = shell
		m.rsvpFocused = false
		return m.refresh(), cmd
	}
	if idx, cmd, hit := m.hitRSVPBtn(msg.X, msg.Y); hit {
		m = m.focusRSVP()
		m.focusedRSVP = idx
		return m.refresh(), cmd
	}
	return m, nil
}

func (m EventDialogModel) hitRSVPBtn(x, y int) (int, tea.Cmd, bool) {
	rsvp := m.rsvpActions()
	if len(rsvp) == 0 {
		return 0, nil, false
	}
	ev, ok := m.selectedEvent()
	if !ok {
		return 0, nil, false
	}
	cal := m.calendars[ev.CalendarID]
	rw := eventMetaURLRewriter(cal)
	att, _ := m.userAttendee()
	rsvpLine := m.renderRSVPLine(att, rsvp, m.detailWidth())
	_, rowIdx := eventDetailLines(ev, cal, m.detailWidth(), m.labelWidth(), rsvpLine, rw)
	if rowIdx < 0 {
		return 0, nil, false
	}

	rsvpY, ok := m.shell.BodyRowScreenY(rowIdx)
	if !ok {
		return 0, nil, false
	}
	ox, _ := m.shell.DetailsOrigin()
	rsvpX := ox + labelColWidth("Your RSVP", m.labelWidth())
	btnW := rsvpButtonWidth()

	if y != rsvpY {
		return 0, nil, false
	}
	cx := rsvpX
	for i, a := range rsvp {
		if x >= cx && x < cx+btnW {
			return i, a.msg, true
		}
		cx += btnW + 1
	}
	return 0, nil, false
}
