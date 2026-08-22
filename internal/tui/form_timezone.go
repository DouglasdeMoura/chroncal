package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// TimezoneField
// ---------------------------------------------------------------------------

// TimezoneField is a focusable field that displays an IANA timezone name. The
// actual timezone selection happens via an overlay managed by the parent; this
// field only renders the current value and toggles a focus highlight.
type TimezoneField struct {
	value   string
	focused bool
}

// NewTimezoneField creates a field that shows the given timezone name.
func NewTimezoneField(tz string) *TimezoneField {
	return &TimezoneField{value: tz}
}

func (f *TimezoneField) Value() string     { return f.value }
func (f *TimezoneField) SetValue(v string) { f.value = v }

func (f *TimezoneField) Update(tea.Msg) tea.Cmd { return nil }
func (f *TimezoneField) View() string {
	s := f.value
	if loc, err := time.LoadLocation(f.value); err == nil {
		_, off := time.Now().In(loc).Zone()
		s += "  (" + formatTZOffset(off) + ")"
	}
	if f.focused {
		return lipgloss.NewStyle().Reverse(true).Render(s)
	}
	return s
}

// formatTZOffset formats a seconds-from-UTC offset as "UTC+HH:MM" or "UTC-HH:MM".
func formatTZOffset(offsetSec int) string {
	sign := "+"
	if offsetSec < 0 {
		sign = "-"
		offsetSec = -offsetSec
	}
	h := offsetSec / 3600
	m := (offsetSec % 3600) / 60
	if m == 0 {
		return fmt.Sprintf("UTC%s%02d", sign, h)
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, h, m)
}
func (f *TimezoneField) Focus() tea.Cmd    { f.focused = true; return nil }
func (f *TimezoneField) Blur()             { f.focused = false }
func (f *TimezoneField) SetWidth(int)      {}
func (f *TimezoneField) IsFocusable() bool { return true }
