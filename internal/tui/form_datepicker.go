package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// DatePickerField
// ---------------------------------------------------------------------------

// DatePickerField is a focusable field that displays a formatted date. The
// actual date selection happens via an overlay managed by the parent; this
// field only renders the current value and toggles a focus highlight.
// When a range end is set, the field renders "Jan 2 → Jan 5, 2026".
type DatePickerField struct {
	value    time.Time
	endValue time.Time
	hasEnd   bool
	focused  bool
}

// NewDatePickerField creates a field that displays the given date.
func NewDatePickerField(value time.Time) *DatePickerField {
	return &DatePickerField{value: value}
}

func (f *DatePickerField) Date() time.Time     { return f.value }
func (f *DatePickerField) SetDate(t time.Time) { f.value = t }

// RangeEnd returns the range end date and whether one is set.
func (f *DatePickerField) RangeEnd() (time.Time, bool) { return f.endValue, f.hasEnd }

// SetRangeEnd records the range end for multi-day events. Pass the end
// date inclusive of the last day.
func (f *DatePickerField) SetRangeEnd(t time.Time) { f.endValue = t; f.hasEnd = true }

// ClearRangeEnd removes any range end. The field then reverts to single-date display.
func (f *DatePickerField) ClearRangeEnd() { f.endValue = time.Time{}; f.hasEnd = false }

func (f *DatePickerField) Value() string { return f.formatted() }

func (f *DatePickerField) formatted() string {
	if !f.hasEnd {
		return f.value.Format("Mon, Jan 2, 2006")
	}
	// Compact range: drop weekday, collapse common year/month where possible.
	start, end := f.value, f.endValue
	if end.Before(start) {
		start, end = end, start
	}
	if start.Year() == end.Year() && start.Month() == end.Month() {
		return fmt.Sprintf("%s %d → %d, %d",
			start.Format("Jan"), start.Day(), end.Day(), start.Year())
	}
	if start.Year() == end.Year() {
		return fmt.Sprintf("%s %d → %s %d, %d",
			start.Format("Jan"), start.Day(), end.Format("Jan"), end.Day(), start.Year())
	}
	return fmt.Sprintf("%s %d, %d → %s %d, %d",
		start.Format("Jan"), start.Day(), start.Year(),
		end.Format("Jan"), end.Day(), end.Year())
}

func (f *DatePickerField) Update(tea.Msg) tea.Cmd { return nil }
func (f *DatePickerField) View() string {
	s := f.formatted()
	if f.focused {
		return lipgloss.NewStyle().Reverse(true).Render(s)
	}
	return s
}
func (f *DatePickerField) Focus() tea.Cmd    { f.focused = true; return nil }
func (f *DatePickerField) Blur()             { f.focused = false }
func (f *DatePickerField) SetWidth(int)      {}
func (f *DatePickerField) IsFocusable() bool { return true }
