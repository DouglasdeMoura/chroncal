package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// SetTheme propagates theme colors to both children so their cursor and focus
// highlights render correctly.
func (m SidebarModel) SetTheme(t Theme) SidebarModel {
	m.miniMonth = m.miniMonth.SetTheme(t.Selected, t.Today, t.Text, t.Muted)
	m.list = m.list.SetTheme(t.Selected, t.Muted, t.Text)
	return m
}

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

// FocusAtStart focuses the sidebar and places focus on the first tab stop:
// the mini-month's previous-month chevron. Use this when entering the sidebar
// via a forward Tab from the main view so tabbing cycles in reading order.
func (m SidebarModel) FocusAtStart() SidebarModel {
	m.focused = true
	m.focus = sidebarFocusMiniMonth
	m.miniMonth = m.miniMonth.FocusFirst()
	return m.refocusChildren()
}

// FocusAtEnd focuses the sidebar and places focus on the last tab stop:
// the "+ Add calendar" row at the bottom of the list. Use this when entering
// the sidebar via a backward Shift+Tab from the main view.
func (m SidebarModel) FocusAtEnd() SidebarModel {
	m.focused = true
	m.focus = sidebarFocusList
	if n := m.list.RowCount(); n > 0 {
		m.list.cursor = n - 1
	}
	return m.refocusChildren()
}

// Blur releases focus. Inner focus of the mini-month is preserved so that
// direct focus via the `s` toggle retains the user's last position; forward
// and backward Tab entry override this via FocusAtStart / FocusAtEnd.
func (m SidebarModel) Blur() SidebarModel {
	m.focused = false
	return m.refocusChildren()
}
func (m SidebarModel) Focused() bool { return m.focused }

func (m SidebarModel) MiniMonth() MiniMonthModel { return m.miniMonth }
func (m SidebarModel) List() CalendarListModel   { return m.list }

// SetList replaces the calendar list child (e.g. after calendars reload).
// Focus state is preserved.
func (m SidebarModel) SetList(l CalendarListModel) SidebarModel {
	m.list = l
	return m.refocusChildren()
}

// SetMiniMonth replaces the mini-month child (e.g. after refreshing the
// per-day event-density set). Focus state is preserved.
func (m SidebarModel) SetMiniMonth(mm MiniMonthModel) SidebarModel {
	m.miniMonth = mm
	return m.refocusChildren()
}

func (m SidebarModel) SetSize(w, h int) SidebarModel {
	m.width = w
	m.height = h
	return m
}

// refocusChildren syncs each child's focused state with the sidebar's
// current focus target so only one child highlights at a time.
func (m SidebarModel) refocusChildren() SidebarModel {
	if m.focused && m.focus == sidebarFocusMiniMonth {
		m.miniMonth = m.miniMonth.Focus()
	} else {
		m.miniMonth = m.miniMonth.Blur()
	}
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

	// Tab / Shift+Tab cross-child routing. The mini-month has three tab
	// stops of its own (prev chevron, grid, next chevron); Tab advances
	// through them before escaping to the calendar list.
	switch kp.String() {
	case "tab":
		switch m.focus {
		case sidebarFocusMiniMonth:
			if !m.miniMonth.AtEnd() {
				m.miniMonth = m.miniMonth.AdvanceFocus()
				return m, nil
			}
			m.focus = sidebarFocusList
			m.miniMonth = m.miniMonth.FocusFirst()
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
				m.miniMonth = m.miniMonth.FocusLast()
				m = m.refocusChildren()
				return m, nil
			}
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		case sidebarFocusMiniMonth:
			if !m.miniMonth.AtStart() {
				m.miniMonth = m.miniMonth.RetreatFocus()
				return m, nil
			}
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

// HandleClick dispatches a mouse click given in the sidebar's local
// coordinates (0,0 == top-left of the mini-month) to the appropriate child.
func (m SidebarModel) HandleClick(x, y int) (SidebarModel, tea.Cmd) {
	// The mini-month always occupies the top of the sidebar. Its rendered
	// height is header (1) + weekday row (1) + up to 6 grid rows = 8.
	const miniMonthMaxY = 8
	if y < miniMonthMaxY {
		mm, cmd := m.miniMonth.HandleClick(x, y)
		m.miniMonth = mm
		return m, cmd
	}
	// View() separates the two children with "\n\n", which — on top of the
	// mini-month's trailing newline — renders as two blank rows before the
	// calendar list begins.
	const listStartY = miniMonthMaxY + 2
	listY := y - listStartY
	if listY < 0 {
		return m, nil
	}
	m.focus = sidebarFocusList
	list, cmd := m.list.HandleClick(x, listY)
	m.list = list
	m = m.refocusChildren()
	return m, cmd
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
