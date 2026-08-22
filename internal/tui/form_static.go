package tui

import (
	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// StaticField
// ---------------------------------------------------------------------------

// StaticField is a non-focusable, display-only form field. It renders its
// value through an optional style function and ignores all input.
type StaticField struct {
	value   string
	styleFn func(string) string
}

// NewStaticField creates a display-only field. styleFn is applied to the value
// on every View call; pass nil for unstyled output.
func NewStaticField(value string, styleFn func(string) string) *StaticField {
	if styleFn == nil {
		styleFn = func(s string) string { return s }
	}
	return &StaticField{value: value, styleFn: styleFn}
}

func (f *StaticField) Value() string     { return f.value }
func (f *StaticField) SetValue(v string) { f.value = v }

func (f *StaticField) Update(tea.Msg) tea.Cmd { return nil }
func (f *StaticField) View() string           { return f.styleFn(f.value) }
func (f *StaticField) Focus() tea.Cmd         { return nil }
func (f *StaticField) Blur()                  {}
func (f *StaticField) SetWidth(int)           {}
func (f *StaticField) IsFocusable() bool      { return false }
