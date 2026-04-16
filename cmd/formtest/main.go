package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/tui"
)

type model struct {
	dialogs []tui.Dialog
	width   int
	height  int
}

func newModel() model {
	hasDark := lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	theme := tui.NewTheme(hasDark)

	ds := tui.DefaultDialogStyles()

	// ── 1. Default: content-sized, no title ──
	d1 := tui.NewDialog("", ds)

	// ── 2. With title and footer ──
	titled := ds
	titled.Title = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
	d2 := tui.NewDialog("New calendar", titled)
	d2.SetFooter("tab next field · enter confirm · esc cancel")

	// ── 3. Warning/error style ──
	warn := ds
	warn.Title = lipgloss.NewStyle().Bold(true).Foreground(theme.Error)
	d3 := tui.NewDialog("Discard unsaved changes?", warn)

	// ── 4. Fixed width ──
	d4 := tui.NewDialog("Fixed width (60)", ds)
	d4.SetWidth(60)

	// ── 5. Success style ──
	success := ds
	success.BorderFg = lipgloss.Color("10")
	d5 := tui.NewDialog("", success)

	return model{dialogs: []tui.Dialog{d1, d2, d3, d4, d5}}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		for i := range m.dialogs {
			m.dialogs[i] = m.dialogs[i].Update(msg)
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) View() tea.View {
	v := tea.View{AltScreen: true}
	if m.width == 0 {
		return v
	}

	faint := lipgloss.NewStyle().Faint(true)

	boxes := []string{
		// 1. Default
		faint.Render("Default (content-sized)"),
		m.dialogs[0].Box("Hello, this is a simple dialog."),
		"",

		// 2. Title + footer
		faint.Render("Title + footer"),
		m.dialogs[1].Box(
			lipgloss.NewStyle().Faint(true).Render("Name") + "  > " + lipgloss.NewStyle().Faint(true).Render("My calendar") + "\n" +
				lipgloss.NewStyle().Faint(true).Render("Color") + " " + lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("●") + " " +
				lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render("●") + " " +
				lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Render("●"),
		),
		"",

		// 3. Warning
		faint.Render("Warning (centered content)"),
		m.dialogs[2].Box("This action cannot be undone."),
		"",

		// 4. Fixed width
		faint.Render("Fixed width (60)"),
		m.dialogs[3].Box("This box is always 60 columns wide\nregardless of content length."),
		"",

		// 5. Success
		faint.Render("Success border"),
		m.dialogs[4].Box(tui.Glyphs["status.ok"] + " Changes saved successfully.\n\nPress q to quit."),
	}

	combined := lipgloss.JoinVertical(lipgloss.Center, boxes...)
	placed := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, combined)

	v.Content = placed
	return v
}

func main() {
	p := tea.NewProgram(newModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
