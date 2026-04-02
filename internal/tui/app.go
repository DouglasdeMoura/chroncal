package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/chroncal/internal/app"
)

type Model struct {
	app    *app.App
	width  int
	height int
}

func NewModel(a *app.App) Model {
	return Model{app: a}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(DefaultTheme.Primary).
		Render("chroncal")

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, title)
}

func Run(a *app.App) error {
	model := NewModel(a)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
