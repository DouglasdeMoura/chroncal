package tui

import tea "charm.land/bubbletea/v2"

// ChoiceDialogResultMsg is emitted when the user picks an option or cancels.
// Choice is -1 when cancelled, otherwise the index of the selected option.
type ChoiceDialogResultMsg struct {
	Choice int
}

// ChoiceDialogModel shows a centered prompt with N option buttons plus Cancel.
type ChoiceDialogModel struct {
	dialog buttonDialogModel
}

func NewChoiceDialogModel(message string, options ...string) ChoiceDialogModel {
	labels := append(append([]string(nil), options...), "Cancel")
	return ChoiceDialogModel{
		dialog: newButtonDialogModel(message, len(labels)-1, labels...),
	}
}

func (m ChoiceDialogModel) SetSize(w, h int) ChoiceDialogModel {
	m.dialog = m.dialog.SetSize(w, h)
	return m
}

func (m ChoiceDialogModel) BoxSize() (int, int) {
	return m.dialog.BoxSize()
}

func (m ChoiceDialogModel) Update(msg tea.Msg) (ChoiceDialogModel, tea.Cmd) {
	dialog, choice, ok := m.dialog.Update(msg)
	m.dialog = dialog
	if !ok {
		return m, nil
	}
	if choice == m.dialog.cancelIndex() {
		choice = -1
	}
	return m, func() tea.Msg { return ChoiceDialogResultMsg{Choice: choice} }
}

func (m ChoiceDialogModel) View() string {
	return m.dialog.View()
}
