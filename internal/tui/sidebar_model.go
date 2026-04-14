package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// SidebarFocusEscapedMsg is emitted when the user Tabs (or Shift+Tabs) past
// the edge of the sidebar's focusable widgets. The parent should move
// top-level focus to the main view.
type SidebarFocusEscapedMsg struct{ Forward bool }

type sidebarChild int

const (
	sidebarFocusMiniMonth sidebarChild = iota
	sidebarFocusList
)

// SidebarModel composes the mini-month picker and the calendar list into a
// single focusable panel. Tab/Shift+Tab moves focus between children and,
// eventually, out to the main view via SidebarFocusEscapedMsg.
type SidebarModel struct {
	miniMonth MiniMonthModel
	list      CalendarListModel
	focus     sidebarChild
	focused   bool
	width     int
	height    int
}

func NewSidebarModel(mm MiniMonthModel, list CalendarListModel) SidebarModel {
	return SidebarModel{
		miniMonth: mm,
		list:      list,
		focus:     sidebarFocusMiniMonth,
	}
}

func (m SidebarModel) Focus() SidebarModel { m.focused = true; return m.refocusChildren() }
func (m SidebarModel) Blur() SidebarModel  { m.focused = false; return m.refocusChildren() }
func (m SidebarModel) Focused() bool       { return m.focused }

func (m SidebarModel) MiniMonth() MiniMonthModel { return m.miniMonth }
func (m SidebarModel) List() CalendarListModel   { return m.list }

// SetList replaces the calendar list child (e.g. after calendars reload).
// Focus state is preserved.
func (m SidebarModel) SetList(l CalendarListModel) SidebarModel {
	m.list = l
	return m.refocusChildren()
}

func (m SidebarModel) SetSize(w, h int) SidebarModel {
	m.width = w
	m.height = h
	return m
}

// refocusChildren syncs the list child's focused state with the sidebar's
// current focus target.
func (m SidebarModel) refocusChildren() SidebarModel {
	if m.focused && m.focus == sidebarFocusList {
		m.list = m.list.Focus()
	} else {
		m.list = m.list.Blur()
	}
	return m
}

func (m SidebarModel) Update(msg tea.Msg) (SidebarModel, tea.Cmd) {
	if !m.focused {
		return m, nil
	}
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	// Tab / Shift+Tab cross-child routing.
	switch kp.String() {
	case "tab":
		switch m.focus {
		case sidebarFocusMiniMonth:
			m.focus = sidebarFocusList
			m = m.refocusChildren()
			m.list.cursor = 0
			return m, nil
		case sidebarFocusList:
			if m.list.cursor >= m.list.RowCount()-1 {
				return m, func() tea.Msg { return SidebarFocusEscapedMsg{Forward: true} }
			}
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
	case "shift+tab":
		switch m.focus {
		case sidebarFocusList:
			if m.list.cursor == 0 {
				m.focus = sidebarFocusMiniMonth
				m = m.refocusChildren()
				return m, nil
			}
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		case sidebarFocusMiniMonth:
			return m, func() tea.Msg { return SidebarFocusEscapedMsg{Forward: false} }
		}
	}

	// All other keys route to the focused child.
	switch m.focus {
	case sidebarFocusMiniMonth:
		var cmd tea.Cmd
		m.miniMonth, cmd = m.miniMonth.Update(msg)
		return m, cmd
	case sidebarFocusList:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m SidebarModel) View() string {
	var b strings.Builder
	b.WriteString(m.miniMonth.View())
	b.WriteString("\n\n")
	b.WriteString(m.list.View())
	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Render(b.String())
}
