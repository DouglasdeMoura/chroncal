package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
)

type eventsLoadedMsg struct {
	events []recurrence.ExpandedEvent
	err    error
}

type Model struct {
	app    *app.App
	theme  Theme
	width  int
	height int
	month  time.Time
	events []recurrence.ExpandedEvent
	err    error
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
		events, err := m.app.Recurrences.ListExpandedEvents(context.Background(), from, to)
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
		return m, nil

	case eventsLoadedMsg:
		m.events = msg.events
		m.err = msg.err
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

const sidebarWidth = 30

func getMainContent(m Model) string {
	var mainContent string
	if m.err != nil {
		mainContent = lipgloss.NewStyle().Foreground(m.theme.Error).Render("Error: " + m.err.Error())
	} else if len(m.events) == 0 {
		mainContent = "No events for " + m.month.Format("January 2006")
	} else {
		mainContent = lipgloss.NewStyle().Bold(true).Render(m.month.Format("January 2006")) + "\n\n"
		for _, ev := range m.events {
			t := ev.InstanceTime.Local().Format("02 Mon 15:04")
			mainContent += t + "  " + ev.Title + "\n"
		}
	}

	return mainContent
}

func (m Model) View() tea.View {
	v := tea.View{AltScreen: true}

	if m.width == 0 {
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
		Render(getMainContent(m))

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
