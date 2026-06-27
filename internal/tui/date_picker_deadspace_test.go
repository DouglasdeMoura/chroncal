package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestHandleDatePickerMouse_DeadSpaceBelowGridIgnored is a regression test for
// issue #469. The mini-month grid renders day rows at content rows 2..7
// (miniMonthGridRows=6). Below it the overlay draws blank/checkbox/status/
// separator/button rows. A left-half click on a dead-space row that is neither
// the checkbox row nor the button row (e.g. row 8, the blank line) must not
// commit the selection or pin a range endpoint.
func TestHandleDatePickerMouse_DeadSpaceBelowGridIgnored(t *testing.T) {
	const screenW, screenH = 120, 40
	day := time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local)

	t.Run("non-range does not commit/close", func(t *testing.T) {
		m, _ := NewEventFormModel(day, map[int64]CalendarInfo{1: {}}, NewTheme(true))
		m = m.SetSize(screenW, screenH)
		m.openDatePicker()

		// Content row mmY=8 (blank line directly below the grid), left half.
		boxW, boxH := m.DatePickerBoxSize()
		ox := (screenW - boxW) / 2
		oy := (screenH - boxH) / 2
		msg := tea.MouseClickMsg(tea.Mouse{X: ox + 2, Y: oy + 2 + 8, Button: tea.MouseLeft})

		got, _ := m.handleDatePickerMouse(msg)
		if !got.datePickerOpen {
			t.Error("dead-space click below grid must not commit/close the picker")
		}
	})

	t.Run("range mode does not pin endpoint", func(t *testing.T) {
		m, _ := NewEventFormModel(day, map[int64]CalendarInfo{1: {}}, NewTheme(true))
		m = m.SetSize(screenW, screenH)
		m.openDatePicker()
		m.toggleRangeMode() // pins start, arms end pin

		boxW, boxH := m.DatePickerBoxSize()
		ox := (screenW - boxW) / 2
		oy := (screenH - boxH) / 2
		msg := tea.MouseClickMsg(tea.Mouse{X: ox + 2, Y: oy + 2 + 8, Button: tea.MouseLeft})

		got, _ := m.handleDatePickerMouse(msg)
		if !got.rangeEnd.IsZero() {
			t.Errorf("dead-space click must not pin a range endpoint; rangeEnd = %v", got.rangeEnd)
		}
	})
}

// TestHandleEndsDatePickerMouse_DeadSpaceBelowGridIgnored covers the sibling
// ends-date picker handler for issue #469.
func TestHandleEndsDatePickerMouse_DeadSpaceBelowGridIgnored(t *testing.T) {
	const screenW, screenH = 120, 40
	start := time.Date(2026, 4, 24, 0, 0, 0, 0, time.Local)
	m, _ := NewEventFormModel(start, map[int64]CalendarInfo{1: {}}, NewTheme(true))
	m = m.SetSize(screenW, screenH)
	m.openEndsDatePicker()
	original := m.endsDate

	// Content row mmY=8 (blank line below the grid in the ends-date layout).
	boxW, boxH := m.DatePickerBoxSize()
	ox := (screenW - boxW) / 2
	oy := (screenH - boxH) / 2
	msg := tea.MouseClickMsg(tea.Mouse{X: ox + 2, Y: oy + 2 + 8, Button: tea.MouseLeft})

	got, _ := m.handleEndsDatePickerMouse(msg)
	if !got.endsDatePicker {
		t.Error("dead-space click below grid must not commit/close the ends-date picker")
	}
	if !got.endsDate.Equal(original) {
		t.Errorf("dead-space click must not change endsDate; got %v want %v", got.endsDate, original)
	}
}
