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

// ConfirmDialogModel shows a centered confirmation prompt with Cancel and
// a caller-defined confirm button. Reusable for any destructive action.
type ConfirmDialogModel struct {
	dialog       Dialog
	form         Form
	contentWidth *int // shared with styleFn closure for centering
}

func NewConfirmDialogModel(message, confirmLabel string) ConfirmDialogModel {
	styles := DefaultDialogStyles()
	dialog := NewDialog("", styles)

	formStyles := DefaultFormStyles()
	formStyles.ButtonAlign = ButtonAlignCenter
	formStyles.LabelLayout = LabelTop

	cw := new(int)
	form := NewForm(confirmLabel, formStyles,
		FormItem{
			Field: NewStaticField(message, func(s string) string {
				if *cw > 0 {
					return lipgloss.NewStyle().Width(*cw).Align(lipgloss.Center).Render(s)
				}
				return s
			}),
		},
	)
	form.OnSubmit(func(f *Form) tea.Cmd {
		return func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: true} }
	})
	form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: false} }
	})

	return ConfirmDialogModel{dialog: dialog, form: form, contentWidth: cw}
}

func (m ConfirmDialogModel) SetSize(w, h int) ConfirmDialogModel {
	const maxWidth = 50
	dw := w
	if dw > maxWidth {
		dw = maxWidth
	}
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: dw, Height: h})
	m.dialog.SetWidth(dw)
	cw := m.dialog.ContentWidth()
	if m.contentWidth != nil {
		*m.contentWidth = cw
	}
	m.form.SetWidth(cw)
	return m
}

func (m ConfirmDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m ConfirmDialogModel) Update(msg tea.Msg) (ConfirmDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	// Esc → cancel.
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if key.Matches(msg, key.NewBinding(key.WithKeys("esc"))) {
			return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: false} }
		}
		// Y/N keyboard shortcuts.
		switch msg.String() {
		case "y", "Y":
			return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: true} }
		case "n", "N":
			return m, func() tea.Msg { return ConfirmDialogResultMsg{Confirmed: false} }
		}
	}

	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m ConfirmDialogModel) View() string {
	return m.dialog.Box(m.form.View())
}
