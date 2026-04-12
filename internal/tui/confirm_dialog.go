package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ConfirmDialogResultMsg is emitted when the user confirms or cancels.
type ConfirmDialogResultMsg struct {
	Confirmed bool
}

type confirmDialogKeyMap struct {
	LeftRight  key.Binding
	Tab        key.Binding
	EnterSpace key.Binding
	Yes        key.Binding
	No         key.Binding
	Close      key.Binding
}

// ConfirmDialogModel shows a centered confirmation prompt with Cancel and
// a caller-defined confirm button. Reusable for any destructive action.
type ConfirmDialogModel struct {
	message      string
	confirmLabel string
	selectedNo   bool // true when Cancel is selected (safe default)
	keys         confirmDialogKeyMap
	width        int
	height       int
}

func NewConfirmDialogModel(message, confirmLabel string) ConfirmDialogModel {
	return ConfirmDialogModel{
		message:      message,
		confirmLabel: confirmLabel,
		selectedNo:   true,
		keys: confirmDialogKeyMap{
			LeftRight:  key.NewBinding(key.WithKeys("left", "right")),
			Tab:        key.NewBinding(key.WithKeys("tab")),
			EnterSpace: key.NewBinding(key.WithKeys("enter", " ")),
			Yes:        key.NewBinding(key.WithKeys("y", "Y")),
			No:         key.NewBinding(key.WithKeys("n", "N")),
			Close:      key.NewBinding(key.WithKeys("esc")),
		},
	}
}

func (m ConfirmDialogModel) SetSize(w, h int) ConfirmDialogModel {
	m.width = w
	m.height = h
	return m
}

func (m ConfirmDialogModel) BoxSize() (int, int) {
	view := m.View()
	return lipgloss.Size(view)
}

func (m ConfirmDialogModel) Update(msg tea.Msg) (ConfirmDialogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m ConfirmDialogModel) handleKey(msg tea.KeyPressMsg) (ConfirmDialogModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close), key.Matches(msg, m.keys.No):
		return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: false} }
	case key.Matches(msg, m.keys.Yes):
		return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: true} }
	case key.Matches(msg, m.keys.LeftRight), key.Matches(msg, m.keys.Tab):
		m.selectedNo = !m.selectedNo
	case key.Matches(msg, m.keys.EnterSpace):
		confirmed := !m.selectedNo
		return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: confirmed} }
	}
	return m, nil
}

func (m ConfirmDialogModel) handleMouse(msg tea.MouseClickMsg) (ConfirmDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}

	ox, oy := m.buttonBarOrigin()
	if msg.Y != oy {
		return m, nil
	}

	confirmBtn := button(m.confirmLabel, 0, false)
	confirmW := lipgloss.Width(confirmBtn)
	if msg.X >= ox && msg.X < ox+confirmW {
		return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: true} }
	}

	cancelBtn := button("Cancel", 0, false)
	cancelX := ox + confirmW + 1
	cancelW := lipgloss.Width(cancelBtn)
	if msg.X >= cancelX && msg.X < cancelX+cancelW {
		return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: false} }
	}

	return m, nil
}

func (m ConfirmDialogModel) buttonBarOrigin() (int, int) {
	boxW, boxH := m.BoxSize()
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2

	confirmBtn := button(m.confirmLabel, 0, false)
	cancelBtn := button("Cancel", 0, false)
	buttonsW := lipgloss.Width(confirmBtn) + 1 + lipgloss.Width(cancelBtn)
	contentW := boxW - 4
	centerOffset := (contentW - buttonsW) / 2

	return dialogX + 2 + centerOffset, dialogY + boxH - 2
}

func (m ConfirmDialogModel) View() string {
	confirmBtn := button(m.confirmLabel, 0, !m.selectedNo)
	cancelBtn := button("Cancel", 0, m.selectedNo)
	buttonsLine := confirmBtn + " " + cancelBtn

	content := lipgloss.JoinVertical(lipgloss.Center, m.message, "", buttonsLine)

	return lipgloss.NewStyle().
		Padding(1, 3).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}
