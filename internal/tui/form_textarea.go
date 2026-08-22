package tui

import (
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// TextAreaField
// ---------------------------------------------------------------------------

// TextAreaField wraps a bubbles textarea for multi-line text input.
type TextAreaField struct {
	input textarea.Model
}

func NewTextAreaField(placeholder string) *TextAreaField {
	input := textarea.New()
	input.Prompt = ""
	input.Placeholder = placeholder
	input.CharLimit = 500
	input.ShowLineNumbers = false
	input.SetHeight(3)
	hint := lipgloss.NewStyle().Italic(true).Faint(true)
	s := input.Styles()
	s.Focused.Placeholder = hint
	s.Blurred.Placeholder = hint
	input.SetStyles(s)
	return &TextAreaField{input: input}
}

func (f *TextAreaField) Value() string     { return f.input.Value() }
func (f *TextAreaField) SetValue(v string) { f.input.SetValue(v) }

func (f *TextAreaField) SetPlaceholder(p string) { f.input.Placeholder = p }
func (f *TextAreaField) SetCharLimit(n int)      { f.input.CharLimit = n }
func (f *TextAreaField) SetHeight(h int)         { f.input.SetHeight(h) }

func (f *TextAreaField) Update(msg tea.Msg) tea.Cmd {
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		k := kp.Key()
		// Block plain Enter so the Form can use it for focus cycling.
		// Shift+Enter inserts a newline by forwarding as a plain Enter.
		if k.Code == '\r' {
			if k.Mod&tea.ModShift == 0 {
				return nil
			}
			plain := tea.Key{Code: '\r'}
			msg = tea.KeyPressMsg(plain)
		}
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

func (f *TextAreaField) View() string      { return f.input.View() }
func (f *TextAreaField) Focus() tea.Cmd    { return f.input.Focus() }
func (f *TextAreaField) Blur()             { f.input.Blur() }
func (f *TextAreaField) SetWidth(w int)    { f.input.SetWidth(w) }
func (f *TextAreaField) IsFocusable() bool { return true }
