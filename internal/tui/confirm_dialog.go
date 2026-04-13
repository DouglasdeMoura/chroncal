package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// ConfirmDialogResultMsg is emitted when the user confirms or cancels.
type ConfirmDialogResultMsg struct {
	Confirmed bool
}

// ConfirmDialogModel shows a centered confirmation prompt with Cancel and
// a caller-defined confirm button. Reusable for any destructive action.
type ConfirmDialogModel struct {
	dialog buttonDialogModel
}

func NewConfirmDialogModel(message, confirmLabel string) ConfirmDialogModel {
	dialog := newButtonDialogModel(message, 1, confirmLabel, "Cancel")
	dialog.extraShortcuts = map[int][]key.Binding{
		0: {key.NewBinding(key.WithKeys("y", "Y"))},
		1: {key.NewBinding(key.WithKeys("n", "N"))},
	}
	return ConfirmDialogModel{dialog: dialog}
}

func (m ConfirmDialogModel) SetSize(w, h int) ConfirmDialogModel {
	m.dialog = m.dialog.SetSize(w, h)
	return m
}

func (m ConfirmDialogModel) BoxSize() (int, int) {
	return m.dialog.BoxSize()
}

func (m ConfirmDialogModel) Update(msg tea.Msg) (ConfirmDialogModel, tea.Cmd) {
	dialog, choice, ok := m.dialog.Update(msg)
	m.dialog = dialog
	if !ok {
		return m, nil
	}
	return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: choice == 0} }
}

func (m ConfirmDialogModel) View() string {
	return m.dialog.View()
}
