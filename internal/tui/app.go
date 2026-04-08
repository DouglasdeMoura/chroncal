package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
)

type eventsLoadedMsg struct {
	events []event.Event
	err    error
}

type Model struct {
	app      *app.App
	theme    Theme
	width    int
	height   int
	month    time.Time
	events   []event.Event
	err      error
	ready    bool
	viewport viewport.Model
}

func NewModel(a *app.App) Model {
	now := time.Now()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return Model{app: a, month: month}
}

func (m Model) loadEvents() tea.Cmd {
	return func() tea.Msg {
		from := m.month
		to := from.AddDate(0, 1, 0)
		expanded, err := m.app.Recurrences.ListExpandedEvents(context.Background(), from, to)
		events := make([]event.Event, len(expanded))
		for i, e := range expanded {
			events[i] = e.Event
		}
		return eventsLoadedMsg{events: events, err: err}
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.loadEvents())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.theme = NewTheme(msg.IsDark())
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		footerHeight := 1
		padding := 1
		borderWidth := 1
		contentHeight := m.height - footerHeight - padding*2
		mainWidth := m.width - sidebarWidth - borderWidth - padding*2

		if !m.ready {
			m.viewport = viewport.New(viewport.WithWidth(mainWidth), viewport.WithHeight(contentHeight))
			m.viewport.SetContent(getMainContent(m))
			m.ready = true
		} else {
			m.viewport.SetWidth(mainWidth)
			m.viewport.SetHeight(contentHeight)
		}
		return m, nil

	case eventsLoadedMsg:
		m.events = msg.events
		m.err = msg.err
		if m.ready {
			m.viewport.SetContent(getMainContent(m))
		}
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

const sidebarWidth = 30

func getMainContent(m Model) string {
	var mainContent string
	if m.err != nil {
		mainContent = lipgloss.NewStyle().Foreground(m.theme.Error).Render("Error: " + m.err.Error())
	} else if len(m.events) == 0 {
		mainContent = "No events for " + m.month.Format("January 2006")
	} else {
		mainContent = FormatEventList(FormatEventListOptions{
			Events:      m.events,
			ShowHeader:  true,
			ShowAllDays: true,
			From:        m.month,
			To:          m.month.AddDate(0, 1, 0),
		})
	}

	return mainContent
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
	borderWidth := 1
	footerHeight := 1
	contentHeight := m.height - footerHeight - padding*2
	mainWidth := m.width - sidebarWidth - borderWidth - padding*2

	sidebar := lipgloss.NewStyle().
		Width(sidebarWidth - padding*2).
		Height(contentHeight).
		Padding(padding).
		BorderRight(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(m.theme.Border).
		Foreground(m.theme.Text).
		Render("Sidebar")

	main := lipgloss.NewStyle().
		Width(mainWidth).
		Height(contentHeight).
		Padding(padding).
		Foreground(m.theme.Text).
		Render(m.viewport.View())

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)

	footer := lipgloss.NewStyle().
		Width(m.width - padding*2).
		Height(footerHeight).
		Padding(padding).
		Foreground(m.theme.TextDim).
		Render("chroncal")

	v.Content = lipgloss.JoinVertical(lipgloss.Left, body, footer)
	return v
}

func Run(a *app.App) error {
	model := NewModel(a)
	p := tea.NewProgram(model)
	_, err := p.Run()
	return err
}
