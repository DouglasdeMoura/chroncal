package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ChoiceDialogResultMsg is emitted when the user picks an option or cancels.
// Choice is -1 when cancelled, otherwise the index of the selected option.
type ChoiceDialogResultMsg struct {
	Choice int
}

type choiceDialogKeyMap struct {
	LeftRight  key.Binding
	Tab        key.Binding
	EnterSpace key.Binding
	Close      key.Binding
}

// ChoiceDialogModel shows a centered prompt with N option buttons plus Cancel.
type ChoiceDialogModel struct {
	message  string
	options  []string
	selected int // index into options; len(options) = Cancel
	keys     choiceDialogKeyMap
	width    int
	height   int
}

func NewChoiceDialogModel(message string, options ...string) ChoiceDialogModel {
	return ChoiceDialogModel{
		message:  message,
		options:  options,
		selected: len(options), // Cancel selected by default
		keys: choiceDialogKeyMap{
			LeftRight:  key.NewBinding(key.WithKeys("left", "right")),
			Tab:        key.NewBinding(key.WithKeys("tab")),
			EnterSpace: key.NewBinding(key.WithKeys("enter", " ")),
			Close:      key.NewBinding(key.WithKeys("esc")),
		},
	}
}

func (m ChoiceDialogModel) SetSize(w, h int) ChoiceDialogModel {
	m.width = w
	m.height = h
	return m
}

func (m ChoiceDialogModel) BoxSize() (int, int) {
	view := m.View()
	return lipgloss.Size(view)
}

func (m ChoiceDialogModel) Update(msg tea.Msg) (ChoiceDialogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m ChoiceDialogModel) total() int { return len(m.options) + 1 }

func (m ChoiceDialogModel) handleKey(msg tea.KeyPressMsg) (ChoiceDialogModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return ChoiceDialogResultMsg{Choice: -1} }
	case key.Matches(msg, m.keys.LeftRight), key.Matches(msg, m.keys.Tab):
		m.selected = (m.selected + 1) % m.total()
	case key.Matches(msg, m.keys.EnterSpace):
		choice := m.selected
		if choice == len(m.options) {
			choice = -1
		}
		return m, func() tea.Msg { return ChoiceDialogResultMsg{Choice: choice} }
	}
	return m, nil
}

func (m ChoiceDialogModel) handleMouse(msg tea.MouseClickMsg) (ChoiceDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}

	ox, oy := m.buttonBarOrigin()
	if msg.Y != oy {
		return m, nil
	}

	x := ox
	for i, label := range m.options {
		w := lipgloss.Width(button(label, 0, false))
		if msg.X >= x && msg.X < x+w {
			choice := i
			return m, func() tea.Msg { return ChoiceDialogResultMsg{Choice: choice} }
		}
		x += w + 1
	}

	cancelW := lipgloss.Width(button("Cancel", 0, false))
	if msg.X >= x && msg.X < x+cancelW {
		return m, func() tea.Msg { return ChoiceDialogResultMsg{Choice: -1} }
	}

	return m, nil
}

func (m ChoiceDialogModel) buttonBarOrigin() (int, int) {
	boxW, boxH := m.BoxSize()
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2

	buttonsW := 0
	for _, label := range m.options {
		buttonsW += lipgloss.Width(button(label, 0, false)) + 1
	}
	buttonsW += lipgloss.Width(button("Cancel", 0, false))
	contentW := boxW - 4
	centerOffset := (contentW - buttonsW) / 2

	return dialogX + 2 + centerOffset, dialogY + boxH - 2
}

func (m ChoiceDialogModel) View() string {
	var buttons string
	for i, label := range m.options {
		btn := button(label, 0, m.selected == i)
		if i > 0 {
			buttons += " "
		}
		buttons += btn
	}
	cancelBtn := button("Cancel", 0, m.selected == len(m.options))
	buttons += " " + cancelBtn

	content := lipgloss.JoinVertical(lipgloss.Center, m.message, "", buttons)

	return lipgloss.NewStyle().
		Padding(1, 3).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}
