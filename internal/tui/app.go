package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/tcal/internal/app"
	"github.com/douglasdemoura/tcal/internal/calendar"
	"github.com/douglasdemoura/tcal/internal/event"
)

type viewMode int

const (
	viewMonth viewMode = iota
	viewWeek
	viewDay
	viewAgenda
)

func (v viewMode) String() string {
	switch v {
	case viewMonth:
		return "Month"
	case viewWeek:
		return "Week"
	case viewDay:
		return "Day"
	case viewAgenda:
		return "Agenda"
	}
	return ""
}

type focus int

const (
	focusCalendar focus = iota
	focusSidebar
	focusDetail
)

// Messages
type eventsLoadedMsg struct {
	events []event.Event
}

type calendarsLoadedMsg struct {
	calendars []calendar.Calendar
}

type eventDeletedMsg struct {
	id int64
}

type errMsg struct {
	err error
}

type Model struct {
	app           *app.App
	view          viewMode
	focus         focus
	month         monthView
	day           dayView
	sidebar       sidebar
	showSidebar   bool
	showDetail    bool
	selectedEvent *event.Event
	calendarMap   map[int64]calendar.Calendar
	width         int
	height        int
	err           error
}

func NewModel(a *app.App) Model {
	return Model{
		app:         a,
		view:        viewMonth,
		focus:       focusCalendar,
		month:       newMonthView(),
		day:         newDayView(),
		sidebar:     newSidebar(),
		showSidebar: true,
		calendarMap: make(map[int64]calendar.Calendar),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadCalendars(), m.loadEvents())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.month.width = msg.Width
		m.month.height = msg.Height
		if m.width < 80 {
			m.showSidebar = false
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case calendarsLoadedMsg:
		m.sidebar.setCalendars(msg.calendars)
		m.calendarMap = make(map[int64]calendar.Calendar)
		for _, c := range msg.calendars {
			m.calendarMap[c.ID] = c
		}
		return m, nil

	case eventsLoadedMsg:
		m.month.setEvents(msg.events)
		m.day.setEvents(m.month.selectedEvents())
		return m, nil

	case eventDeletedMsg:
		m.showDetail = false
		m.selectedEvent = nil
		return m, m.loadEvents()

	case errMsg:
		m.err = msg.err
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys
	if key.Matches(msg, keys.Quit) {
		return m, tea.Quit
	}

	// Detail view keys
	if m.showDetail {
		return m.handleDetailKeys(msg)
	}

	// Sidebar-focused keys
	if m.focus == focusSidebar {
		return m.handleSidebarKeys(msg)
	}

	// View-switching keys
	switch {
	case key.Matches(msg, keys.MonthView):
		m.view = viewMonth
		return m, nil
	case key.Matches(msg, keys.DayView):
		m.view = viewDay
		m.day.setEvents(m.month.selectedEvents())
		return m, nil
	case key.Matches(msg, keys.ToggleSidebar):
		if m.focus == focusCalendar {
			m.focus = focusSidebar
		} else {
			m.focus = focusCalendar
		}
		m.showSidebar = true
		return m, nil
	}

	// View-specific keys
	switch m.view {
	case viewMonth:
		return m.handleMonthKeys(msg)
	case viewDay:
		return m.handleDayKeys(msg)
	}

	return m, nil
}

func (m Model) handleMonthKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	prevMonth := m.month.month
	prevYear := m.month.year

	switch {
	case key.Matches(msg, keys.Left):
		m.month.prevDay()
	case key.Matches(msg, keys.Right):
		m.month.nextDay()
	case key.Matches(msg, keys.Up):
		m.month.prevWeek()
	case key.Matches(msg, keys.Down):
		m.month.nextWeek()
	case key.Matches(msg, keys.NextMonth):
		m.month.nextMonth()
	case key.Matches(msg, keys.PrevMonth):
		m.month.prevMonth()
	case key.Matches(msg, keys.Today):
		m.month.goToToday()
	case key.Matches(msg, keys.Enter):
		m.view = viewDay
		m.day.setEvents(m.month.selectedEvents())
		return m, nil
	case key.Matches(msg, keys.NewEvent):
		// Will be implemented in Phase 5
		return m, nil
	default:
		return m, nil
	}

	m.day.setEvents(m.month.selectedEvents())

	// Reload events if month changed
	if m.month.month != prevMonth || m.month.year != prevYear {
		return m, m.loadEvents()
	}
	return m, nil
}

func (m Model) handleDayKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Up):
		m.day.prevEvent()
	case key.Matches(msg, keys.Down):
		m.day.nextEvent()
	case key.Matches(msg, keys.Left):
		m.month.prevDay()
		m.day.setEvents(m.month.selectedEvents())
		return m, nil
	case key.Matches(msg, keys.Right):
		m.month.nextDay()
		m.day.setEvents(m.month.selectedEvents())
		return m, nil
	case key.Matches(msg, keys.Back):
		m.view = viewMonth
	case key.Matches(msg, keys.Enter):
		if e := m.day.selectedEvent(); e != nil {
			m.selectedEvent = e
			m.showDetail = true
		}
	case key.Matches(msg, keys.NewEvent):
		// Will be implemented in Phase 5
		return m, nil
	}
	return m, nil
}

func (m Model) handleDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Back):
		m.showDetail = false
		m.selectedEvent = nil
	case key.Matches(msg, keys.Delete):
		if m.selectedEvent != nil {
			return m, m.deleteEvent(m.selectedEvent.ID)
		}
	}
	return m, nil
}

func (m Model) handleSidebarKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Up):
		m.sidebar.prev()
	case key.Matches(msg, keys.Down):
		m.sidebar.next()
	case key.Matches(msg, keys.Enter):
		m.sidebar.toggle()
	case key.Matches(msg, keys.ToggleSidebar), key.Matches(msg, keys.Back):
		m.focus = focusCalendar
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	var mainContent string

	if m.showDetail && m.selectedEvent != nil {
		calName := ""
		if c, ok := m.calendarMap[m.selectedEvent.CalendarID]; ok {
			calName = c.Name
		}
		mainContent = panelStyle.Render(renderEventDetail(m.selectedEvent, calName))
	} else {
		switch m.view {
		case viewMonth:
			mainContent = m.month.view()
		case viewDay:
			dateLabel := m.month.selected.Format("Monday, January 2, 2006")
			mainContent = m.day.view(dateLabel)
		default:
			mainContent = fmt.Sprintf("%s view coming soon.", m.view)
		}
		mainContent = panelStyle.Render(mainContent)
	}

	var content string
	if m.showSidebar {
		sidebarWidth := 24
		mainWidth := m.width - sidebarWidth - 4
		if mainWidth < 40 {
			mainWidth = 40
		}
		sb := m.sidebar.view(sidebarWidth)
		main := lipgloss.NewStyle().Width(mainWidth).Render(mainContent)
		content = lipgloss.JoinHorizontal(lipgloss.Top, sb, " ", main)
	} else {
		content = mainContent
	}

	statusBar := renderStatusBar(m.view.String(), m.width)

	// Calculate remaining height for padding
	contentHeight := lipgloss.Height(content)
	statusHeight := lipgloss.Height(statusBar)
	remaining := m.height - contentHeight - statusHeight
	if remaining < 0 {
		remaining = 0
	}

	padding := ""
	if remaining > 0 {
		padding = lipgloss.NewStyle().Height(remaining).Render("")
	}

	return lipgloss.JoinVertical(lipgloss.Left, content, padding, statusBar)
}

// Commands

func (m Model) loadCalendars() tea.Cmd {
	return func() tea.Msg {
		cals, err := m.app.Calendars.List(context.Background())
		if err != nil {
			return errMsg{err}
		}
		return calendarsLoadedMsg{cals}
	}
}

func (m Model) loadEvents() tea.Cmd {
	return func() tea.Msg {
		from, to := m.month.dateRange()
		events, err := m.app.Events.ListByDateRange(context.Background(), from, to)
		if err != nil {
			return errMsg{err}
		}
		return eventsLoadedMsg{events}
	}
}

func (m Model) deleteEvent(id int64) tea.Cmd {
	return func() tea.Msg {
		err := m.app.Events.Delete(context.Background(), id)
		if err != nil {
			return errMsg{err}
		}
		return eventDeletedMsg{id}
	}
}

func Run(a *app.App) error {
	model := NewModel(a)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
