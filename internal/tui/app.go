package tui

import (
	"context"
	"image"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/event"
)

type eventsLoadedMsg struct {
	events []event.Event
	err    error
}

type Model struct {
	app         *app.App
	theme       Theme
	width       int
	height      int
	calendar    CalendarModel
	events      []event.Event
	dialog      EventDialogModel
	dialogOpen  bool
	err         error
	ready       bool
	showSidebar bool
}

func NewModel(a *app.App) Model {
	ui := config.LoadUIState()
	return Model{app: a, calendar: NewCalendarModel(time.Now()), showSidebar: ui.ShowSidebar}
}

func (m Model) loadEvents() tea.Cmd {
	month := m.calendar.Month()
	return func() tea.Msg {
		from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
		to := from.AddDate(0, 1, 0)
		expanded, err := m.app.Recurrences.ListExpandedEvents(context.Background(), from, to)
		events := make([]event.Event, len(expanded))
		for i, e := range expanded {
			events[i] = e.Event
		}
		return eventsLoadedMsg{events: events, err: err}
	}
}

func eventsOn(events []event.Event, day time.Time) []event.Event {
	dayKey := day.Local().Format("2006-01-02")
	var out []event.Event
	for _, e := range events {
		if e.StartTime.Local().Format("2006-01-02") == dayKey {
			out = append(out, e)
		}
	}
	return out
}

func eventsToCalendar(events []event.Event) []CalendarEvent {
	out := make([]CalendarEvent, len(events))
	for i, e := range events {
		out[i] = CalendarEvent{
			Title:  e.Title,
			AllDay: e.AllDay,
			Day:    e.StartTime.Local(),
		}
	}
	return out
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.loadEvents())
}

const sidebarWidth = 30

func (m Model) mainDims() (int, int) {
	padding := 1
	footerHeight := 1
	contentHeight := m.height - footerHeight - padding*2
	mainWidth := m.width - padding*2
	if m.showSidebar {
		mainWidth -= sidebarWidth
	}
	return mainWidth, contentHeight
}

// innerDims returns the space available inside the padded main box,
// which is what the calendar renderer should fill.
func (m Model) innerDims() (int, int) {
	mw, mh := m.mainDims()
	padding := 1
	return mw - padding*2, mh - padding*2
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = NewTheme(msg.IsDark())
		m.calendar = m.calendar.SetSelectedColor(m.theme.Text)
		m.dialog = m.dialog.SetTheme(m.theme)
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		iw, ih := m.innerDims()
		m.calendar = m.calendar.SetSize(iw, ih)
		m.dialog = m.dialog.SetSize(m.width, m.height)
		m.ready = true
		return m, nil

	case eventsLoadedMsg:
		m.err = msg.err
		m.events = msg.events
		m.calendar = m.calendar.SetEvents(eventsToCalendar(msg.events))
		return m, nil

	case CalendarMonthChangedMsg:
		return m, m.loadEvents()

	case CalendarDaySelectedMsg:
		dayEvents := eventsOn(m.events, msg.Day)
		if len(dayEvents) == 0 {
			return m, nil
		}
		m.dialog = NewEventDialogModel(msg.Day, dayEvents, m.theme).
			SetSize(m.width, m.height)
		m.dialogOpen = true
		return m, nil

	case EventDialogClosedMsg:
		m.dialogOpen = false
		return m, nil

	case tea.KeyPressMsg:
		if m.dialogOpen {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.dialog, cmd = m.dialog.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "s":
			m.showSidebar = !m.showSidebar
			iw, ih := m.innerDims()
			m.calendar = m.calendar.SetSize(iw, ih)
			_ = config.SaveUIState(config.UIState{ShowSidebar: m.showSidebar})
			return m, nil
		}
		var cmd tea.Cmd
		m.calendar, cmd = m.calendar.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) View() tea.View {
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion}

	if m.width == 0 {
		return v
	}

	if !m.ready {
		v.Content = "\n  Loading..."
		return v
	}

	padding := 1
	footerHeight := 1
	mainWidth, contentHeight := m.mainDims()

	mainContent := m.calendar.View()
	if m.err != nil {
		mainContent = lipgloss.NewStyle().Foreground(m.theme.Error).Render("Error: " + m.err.Error())
	}

	main := lipgloss.NewStyle().
		Width(mainWidth).
		Height(contentHeight).
		Padding(padding).
		Foreground(m.theme.Text).
		Render(mainContent)

	var body string
	if m.showSidebar {
		sidebar := lipgloss.NewStyle().
			Width(sidebarWidth).
			Height(contentHeight).
			Padding(padding).
			BorderRight(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(m.theme.Border).
			Foreground(m.theme.Text).
			Render("Sidebar")
		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
	} else {
		body = main
	}

	footer := lipgloss.NewStyle().
		Width(m.width - padding*2).
		Height(footerHeight).
		Padding(padding).
		Foreground(m.theme.TextDim).
		Render("chroncal  ·  hjkl/arrows: move  ·  [/]: month  ·  t: today  ·  enter: select  ·  s: sidebar  ·  q: quit")

	v.Content = lipgloss.JoinVertical(lipgloss.Left, body, footer)

	if m.dialogOpen {
		v.Content = m.compositeDialog(v.Content)
	}

	return v
}

// compositeDialog draws the dialog box over the already-rendered main view
// using an ultraviolet screen buffer. The background content outside the
// dialog's rectangle is preserved unchanged.
func (m Model) compositeDialog(background string) string {
	if m.width <= 0 || m.height <= 0 {
		return background
	}

	buf := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString(background).Draw(buf, buf.Bounds())

	dialogView := m.dialog.View()
	if dialogView == "" {
		return buf.Render()
	}

	boxW, boxH := m.dialog.BoxSize()
	if boxW <= 0 || boxH <= 0 {
		return buf.Render()
	}
	x := (m.width - boxW) / 2
	y := (m.height - boxH) / 2
	rect := image.Rect(x, y, x+boxW, y+boxH)
	uv.NewStyledString(dialogView).Draw(buf, rect)

	return buf.Render()
}

func Run(a *app.App) error {
	model := NewModel(a)
	p := tea.NewProgram(model)
	_, err := p.Run()
	return err
}
