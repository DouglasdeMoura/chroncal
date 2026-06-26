package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openRecurrenceEndsPicker drives the real open flow so the picker is opened
// the same way a user does: select "On <date>", focus the Ends field, press
// enter. This ensures any staging/revert state is initialised exactly as in
// production.
func openRecurrenceEndsPicker(t *testing.T, start time.Time) RecurrenceEditorModel {
	t.Helper()
	m := NewRecurrenceEditorModel(start, 120, 40, Theme{})
	m.endsField.SetSelected(int(endsOnDate))
	m.syncFromForm()

	endsIdx := -1
	for i, item := range m.form.items {
		if item.Label == "Ends" {
			endsIdx = i
			break
		}
	}
	require.NotEqual(t, -1, endsIdx, "Ends field must exist")
	m.form.focused = endsIdx

	m, _ = m.Update(keyPressMsg("enter"))
	require.True(t, m.endsDatePicker, "enter on Ends=ondate must open the picker")
	return m
}

// TestRecurrenceEditor_EscRevertsNavigatedEndsDate is the TDD regression test
// for issue #410: arrow-key navigation in the ends-date picker mutated the
// committed date with no staging, so pressing Esc committed the navigated date
// instead of reverting to the original.
func TestRecurrenceEditor_EscRevertsNavigatedEndsDate(t *testing.T) {
	start := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	m := openRecurrenceEndsPicker(t, start)

	original := m.endsDate

	// Navigate away from the original date.
	m, _ = m.Update(keyPressMsg("right"))
	m, _ = m.Update(keyPressMsg("down"))
	require.NotEqual(t, original, m.endsDate, "navigation should move the displayed date")

	// Esc must revert and close.
	m, _ = m.Update(keyPressMsg("esc"))
	assert.False(t, m.endsDatePicker, "esc must close the picker")
	assert.Equal(t, original, m.endsDate,
		"esc must revert the ends date, not commit the navigated value")
}

// TestRecurrenceEditor_MouseCancelRevertsNavigatedEndsDate verifies that
// clicking Cancel after navigating discards the navigation.
func TestRecurrenceEditor_MouseCancelRevertsNavigatedEndsDate(t *testing.T) {
	const screenW, screenH = 120, 40
	start := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	m := openRecurrenceEndsPicker(t, start)

	original := m.endsDate

	m, _ = m.Update(keyPressMsg("right"))
	require.NotEqual(t, original, m.endsDate)

	pickerBoxW, _ := m.EndsDatePickerBoxSize()
	ox := (screenW - pickerBoxW) / 2
	oy := (screenH - 14) / 2
	gridY := oy + 4
	const buttonRowRY = 8
	btnY := gridY + buttonRowRY

	innerW := pickerBoxW - 4
	bs := DefaultButtonStyles()
	cancelW := lipgloss.Width(bs.Normal.Render("Cancel", false))
	btnPad := max(innerW-(cancelW+1+lipgloss.Width(bs.Normal.Render("Ok", false))), 0)
	cancelX := ox + 2 + btnPad

	m, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: cancelX, Y: btnY, Button: tea.MouseLeft}))
	assert.False(t, m.endsDatePicker, "clicking Cancel must close the picker")
	assert.Equal(t, original, m.endsDate, "clicking Cancel must revert the navigated date")
}

// TestRecurrenceEditor_OkCommitsNavigatedEndsDate verifies the positive path:
// tabbing to Ok and confirming commits the navigated date.
func TestRecurrenceEditor_OkCommitsNavigatedEndsDate(t *testing.T) {
	start := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	m := openRecurrenceEndsPicker(t, start)

	original := m.endsDate

	m, _ = m.Update(keyPressMsg("right"))
	navigated := m.endsDate
	require.NotEqual(t, original, navigated)

	// Tab: grid -> Cancel -> Ok, then confirm.
	m, _ = m.Update(keyPressMsg("tab"))
	m, _ = m.Update(keyPressMsg("tab"))
	m, _ = m.Update(keyPressMsg("enter"))

	assert.False(t, m.endsDatePicker, "Ok must close the picker")
	assert.Equal(t, navigated, m.endsDate, "Ok must commit the navigated date")
}
