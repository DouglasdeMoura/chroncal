package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// HelpDialogRequestedMsg requests opening the help dialog.
type HelpDialogRequestedMsg struct{}

// HelpDialogClosedMsg is emitted when the help dialog is dismissed.
type HelpDialogClosedMsg struct{}

// HelpDialogModel is a placeholder help dialog. Content will be fleshed out later.
type HelpDialogModel struct {
	dialog Dialog
	theme  Theme
	width  int
	height int
}

func NewHelpDialogModel(theme Theme) HelpDialogModel {
	dialog := NewDialog("Help", DefaultDialogStyles())
	dialog.SetWidth(50)
	dialog.SetFooter("esc to close")
	return HelpDialogModel{dialog: dialog, theme: theme}
}

func (m HelpDialogModel) SetSize(w, h int) HelpDialogModel {
	m.width = w
	m.height = h
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func (m HelpDialogModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}

func (m HelpDialogModel) Update(msg tea.Msg) (HelpDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if key.Matches(msg, key.NewBinding(key.WithKeys("esc", "q", "?"))) {
			return m, func() tea.Msg { return HelpDialogClosedMsg{} }
		}
	}
	return m, nil
}

func (m HelpDialogModel) View() string {
	body := lipgloss.NewStyle().
		Foreground(m.theme.Muted).
		Render("Help content coming soon.")
	return m.dialog.Box(body)
}
