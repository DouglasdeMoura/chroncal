package tui

import (
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// OpenerField
// ---------------------------------------------------------------------------

// OpenerField is a focusable, display-only field whose Enter handle is
// owned by the parent (typically to open an overlay). Looks like a
// StaticField but participates in the focus cycle.
type OpenerField struct {
	value   string
	focused bool
}

func NewOpenerField(value string) *OpenerField {
	return &OpenerField{value: value}
}

func (f *OpenerField) Value() string     { return f.value }
func (f *OpenerField) SetValue(v string) { f.value = v }

func (f *OpenerField) Update(tea.Msg) tea.Cmd { return nil }
func (f *OpenerField) View() string {
	if f.focused {
		return lipgloss.NewStyle().Reverse(true).Render(f.value)
	}
	return f.value
}
func (f *OpenerField) Focus() tea.Cmd    { f.focused = true; return nil }
func (f *OpenerField) Blur()             { f.focused = false }
func (f *OpenerField) SetWidth(int)      {}
func (f *OpenerField) IsFocusable() bool { return true }
