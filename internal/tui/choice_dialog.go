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

// ChoiceDialogModel shows a centered prompt with N option buttons plus Cancel.
type ChoiceDialogModel struct {
	dialog       Dialog
	form         Form
	choices      int // number of choice buttons (excludes Cancel)
	contentWidth *int
}

const choiceDialogMaxWidth = 72

func NewChoiceDialogModel(message string, options ...string) ChoiceDialogModel {
	styles := DefaultDialogStyles()
	dialog := NewDialog("", styles)

	formStyles := DefaultFormStyles()
	formStyles.ButtonAlign = ButtonAlignCenter
	formStyles.LabelLayout = LabelTop

	cw := new(int)

	// Use the first option as the submit button, the rest as action buttons.
	submitLabel := "OK"
	if len(options) > 0 {
		submitLabel = options[0]
	}

	form := NewForm(submitLabel, formStyles,
		FormItem{
			Field: NewStaticField(message, func(s string) string {
				if *cw > 0 {
					return lipgloss.NewStyle().Width(*cw).Align(lipgloss.Center).Render(s)
				}
				return s
			}),
		},
	)
	for i := 1; i < len(options); i++ {
		idx := i
		form.SetActionButton(options[i], Button, func() tea.Msg {
			return ChoiceDialogResultMsg{Choice: idx}
		})
	}

	form.OnSubmit(func(f *Form) tea.Cmd {
		return func() tea.Msg { return ChoiceDialogResultMsg{Choice: 0} }
	})
	form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return ChoiceDialogResultMsg{Choice: -1} }
	})
	form, _ = form.focusIndex(form.submitIndex())

	return ChoiceDialogModel{dialog: dialog, form: form, choices: len(options), contentWidth: cw}
}

func (m ChoiceDialogModel) SetSize(w, h int) ChoiceDialogModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	dw := w
	if dw > choiceDialogMaxWidth {
		dw = choiceDialogMaxWidth
	}
	m.dialog.SetWidth(dw)
	cw := m.dialog.ContentWidth()
	if m.contentWidth != nil {
		*m.contentWidth = cw
	}
	m.form.SetWidth(cw)
	return m
}

func (m ChoiceDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m ChoiceDialogModel) Update(msg tea.Msg) (ChoiceDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	// Esc → cancel.
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if key.Matches(msg, key.NewBinding(key.WithKeys("esc"))) {
			return m, func() tea.Msg { return ChoiceDialogResultMsg{Choice: -1} }
		}
	}

	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m ChoiceDialogModel) View() string {
	return m.dialog.Box(m.form.View())
}
