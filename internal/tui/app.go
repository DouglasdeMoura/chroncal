package tui

import (
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/app"
)

type Model struct {
	app    *app.App
	theme  Theme
	width  int
	height int
}

func NewModel(a *app.App) Model {
	return Model{app: a}
}

func (m Model) Init() tea.Cmd {
	return tea.RequestBackgroundColor
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

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

const sidebarWidth = 30

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
		Render("Main")

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
