package tui

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (m Model) captureOverlayInput(msg tea.Msg) (Model, tea.Cmd, bool) {
	return m.routeOverlay(msg)
}

func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if !m.dialogOpen && !m.choiceOpen && !m.confirmOpen && !m.oauthFlowOpen {
		switch m.viewMode {
		case viewWeek:
			switch msg.Button {
			case tea.MouseWheelUp:
				m.week.scrollOffset -= m.week.linesPerHour
				if m.week.scrollOffset < 0 {
					m.week.scrollOffset = 0
				}
			case tea.MouseWheelDown:
				m.week.scrollOffset += m.week.linesPerHour
				if ms := m.week.maxScroll(); m.week.scrollOffset > ms {
					m.week.scrollOffset = ms
				}
			}
		case viewDay:
			switch msg.Button {
			case tea.MouseWheelUp:
				m.day.scrollOffset -= m.day.linesPerHour
				if m.day.scrollOffset < 0 {
					m.day.scrollOffset = 0
				}
			case tea.MouseWheelDown:
				m.day.scrollOffset += m.day.linesPerHour
				if ms := m.day.maxScroll(); m.day.scrollOffset > ms {
					m.day.scrollOffset = ms
				}
			}
		case viewAgenda:
			var cmd tea.Cmd
			switch msg.Button {
			case tea.MouseWheelUp:
				cmd = m.agenda.ScrollBy(-agendaWheelStep)
			case tea.MouseWheelDown:
				cmd = m.agenda.ScrollBy(agendaWheelStep)
			}
			return m, cmd
		default:
			// viewMonth: no wheel scrolling
		}
	}
	return m, nil
}

func (m Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	// Footer label hit-test: clicking the context label (MONTH/WEEK/DAY/
	// AGENDA) cycles through views. The sweep in View() populates the
	// tracker with the footer label's zone whenever no overlay is open.
	if target := MouseResolve(msg.X, msg.Y); target == "footer:label" {
		return m.cycleView()
	}
	// Sidebar hit-test. The sidebar content starts at (padding, padding)
	// inside the outer screen, with a 1-col right border. If the click
	// lands inside that x-range we dispatch to the sidebar in its local
	// coordinates instead of the main calendar.
	if m.showSidebar {
		padding := 1
		if msg.X >= padding && msg.X < sidebarWidth-padding {
			localX := msg.X - padding
			localY := msg.Y - padding
			// Moving focus to the sidebar mirrors keyboard navigation;
			// otherwise the chevrons would click but not visibly focus.
			if m.focus != focusSidebar {
				m.focus = focusSidebar
				m.sidebar = m.sidebar.Focus()
			}
			var cmd tea.Cmd
			m.sidebar, cmd = m.sidebar.HandleClick(localX, localY)
			return m, cmd
		}
	}
	// Click landed outside the sidebar. Pull focus back to the main
	// view. Later keystrokes then target the calendar, not the
	// sidebar that was last clicked.
	if m.focus == focusSidebar {
		m.sidebar = m.sidebar.Blur()
		m.focus = focusCalendar
	}
	ox, oy := m.calendarOffset()
	switch m.viewMode {
	case viewDay:
		day, ok := m.day.DayAtPosition(msg.X-ox, msg.Y-oy)
		if !ok {
			return m, nil
		}
		m.clickedEventID = m.day.EventAtPosition(msg.X-ox, msg.Y-oy)
		var cmd tea.Cmd
		m.day, cmd = m.day.selectDay(day)
		return m, cmd
	case viewWeek:
		day, ok := m.week.DayAtPosition(msg.X-ox, msg.Y-oy)
		if !ok {
			return m, nil
		}
		m.clickedEventID = m.week.EventAtPosition(msg.X-ox, msg.Y-oy)
		var cmd tea.Cmd
		m.week, cmd = m.week.selectDay(day)
		return m, cmd
	case viewAgenda:
		var cmd tea.Cmd
		m.agenda, cmd = m.agenda.HandleClick(msg.X-ox, msg.Y-oy)
		return m, cmd
	default:
		day, ok := m.calendar.DayAtPosition(msg.X-ox, msg.Y-oy)
		if !ok {
			return m, nil
		}
		m.clickedEventID = m.calendar.EventAtPosition(msg.X-ox, msg.Y-oy)
		var cmd tea.Cmd
		m.calendar, cmd = m.calendar.selectDay(day)
		return m, cmd
	}
}

func (m Model) handleKeyPress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Palette):
		return m.openPalette()
	case key.Matches(msg, m.keys.MonthView):
		return m.switchToView(viewMonth)
	case key.Matches(msg, m.keys.WeekView):
		return m.switchToView(viewWeek)
	case key.Matches(msg, m.keys.DayView):
		return m.switchToView(viewDay)
	case key.Matches(msg, m.keys.AgendaView):
		return m.switchToView(viewAgenda)
	case key.Matches(msg, m.keys.Sidebar):
		return m.toggleSidebar()
	case key.Matches(msg, m.keys.WeekNumbers):
		return m.toggleWeekNumbers()
	case key.Matches(msg, m.keys.WeekStart):
		return m.toggleWeekStart()
	case key.Matches(msg, m.keys.CalendarList):
		return m, func() tea.Msg { return CalendarManagerRequestedMsg{Target: CalendarManagerTargetRoot} }
	case key.Matches(msg, m.keys.Sync):
		return m, func() tea.Msg { return SyncAllRequestedMsg{} }
	case key.Matches(msg, m.keys.SwitchFocus):
		// Only handle Tab/Shift+Tab at the app level when the user
		// enters the sidebar from the main view. Forward Tab lands on
		// the first sidebar tab stop (the prev-month chevron). Backward
		// Shift+Tab lands on the last (the bottom calendar list row).
		// Once focus is inside the sidebar, the key falls through to
		// m.sidebar.Update. That cycles between its internal stops and
		// emits SidebarFocusEscapedMsg to hand focus back to the main
		// view.
		if m.showSidebar && m.focus != focusSidebar {
			m.focus = focusSidebar
			if msg.String() == "shift+tab" {
				m.sidebar = m.sidebar.FocusAtEnd()
			} else {
				m.sidebar = m.sidebar.FocusAtStart()
			}
			return m, nil
		}
		// Fall through to the sidebar routing below.
	case key.Matches(msg, m.keys.Create):
		var cursor time.Time
		switch m.viewMode {
		case viewDay:
			cursor = m.day.Cursor()
		case viewWeek:
			cursor = m.week.Cursor()
		case viewAgenda:
			cursor = m.agenda.SelectedDay()
		default:
			cursor = m.calendar.Cursor()
		}
		form, cmd := NewEventFormModel(cursor, eventFormCalendars(m.calendars), m.theme)
		return m.openEventForm(form, cmd)
	}
	if m.focus == focusCalendar {
		switch m.viewMode {
		case viewDay:
			var cmd tea.Cmd
			m.day, cmd = m.day.Update(msg)
			return m, cmd
		case viewWeek:
			var cmd tea.Cmd
			m.week, cmd = m.week.Update(msg)
			return m, cmd
		case viewAgenda:
			var cmd tea.Cmd
			m.agenda, cmd = m.agenda.Update(msg)
			return m, cmd
		default:
			var cmd tea.Cmd
			m.calendar, cmd = m.calendar.Update(msg)
			return m, cmd
		}
	}
	if m.focus == focusSidebar {
		var cmd tea.Cmd
		m.sidebar, cmd = m.sidebar.Update(msg)
		return m, cmd
	}
	return m, nil
}
