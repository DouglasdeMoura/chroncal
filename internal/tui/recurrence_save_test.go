package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Regression: custom weekly recurrence (Mon-Thu, never ends) was lost when
// the user navigated to the field with arrow keys and then submitted via
// Ctrl+S, because the form's OnSubmit closure captured a stale receiver.
func TestEventForm_CustomRecurrencePersistsThroughSubmit(t *testing.T) {
	day := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC) // Friday
	m, _ := NewEventFormModel(day, testEventFormCalendars(), Theme{})
	m = m.SetSize(120, 40)
	m.titleField.SetValue("Standup")

	repeatIdx := -1
	for i, key := range m.fieldKeys {
		if key == efKeyRepeat {
			repeatIdx = i
			break
		}
	}
	require.NotEqual(t, -1, repeatIdx)
	m.form.focused = repeatIdx

	// Navigate to "Custom..." via the arrow keys the way a user would.
	for i := 0; i < 7; i++ {
		m, _ = m.Update(keyPressMsg("right"))
	}
	require.Equal(t, repeatCustomIdx, m.repeatField.Selected())

	// Open the editor and configure Mon-Thu, never ends.
	m, _ = m.Update(keyPressMsg("enter"))
	require.True(t, m.rruleEditorOpen)

	var weekDays [7]bool
	weekDays[1] = true
	weekDays[2] = true
	weekDays[3] = true
	weekDays[4] = true
	m.rruleEditor.onField.SetWeekly(weekDays, 1)
	m.rruleEditor.syncFromForm()

	// User confirms the editor with Ctrl+S, then the editor done msg comes
	// back through the program loop.
	m, _ = m.Update(keyPressMsg("ctrl+s"))
	m, _ = m.Update(recurrenceEditorDone)
	require.False(t, m.rruleEditorOpen)
	require.Equal(t, "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH", m.customRule)

	// Now Ctrl+S in the main form. This runs Form.Submit which validates
	// then fires OnSubmit; OnSubmit emits eventFormSubmitNowMsg which the
	// model intercepts to call save() against the live state.
	m, cmd := m.Update(keyPressMsg("ctrl+s"))
	require.NotNil(t, cmd)
	first := cmd()
	// First cmd is the validation+emit; second is the actual save.
	if _, ok := first.(eventFormSubmitNowMsg); ok {
		m, cmd = m.Update(first)
		require.NotNil(t, cmd)
		first = cmd()
	}
	saveMsg, ok := first.(EventFormSaveMsg)
	require.True(t, ok, "expected EventFormSaveMsg, got %T", first)
	require.Equal(t, "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH", saveMsg.RecurrenceRule)
}

// Regression: multi-day range selection via the date picker was lost on
// submit for the same reason the recurrence rule was — value-typed range
// fields (rangeHasEnd, rangeEndDate) live on the caller's m, not on the
// OnSubmit closure's captured receiver.
func TestEventForm_MultiDayRangePersistsThroughSubmit(t *testing.T) {
	start := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC) // Monday
	m, _ := NewEventFormModel(start, testEventFormCalendars(), Theme{})
	m = m.SetSize(120, 40)
	m.titleField.SetValue("Workshop")

	// Simulate committing a multi-day range from the date picker.
	m.rangeMode = true
	m.rangeStart = start
	m.rangeEnd = time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC) // Thursday
	m.commitDatePickerSelection()
	require.True(t, m.rangeHasEnd)

	// Submit via Ctrl+S.
	m, cmd := m.Update(keyPressMsg("ctrl+s"))
	require.NotNil(t, cmd)
	first := cmd()
	if _, ok := first.(eventFormSubmitNowMsg); ok {
		m, cmd = m.Update(first)
		require.NotNil(t, cmd)
		first = cmd()
	}
	saveMsg, ok := first.(EventFormSaveMsg)
	require.True(t, ok)
	// End date should be Thursday (or later, depending on time-of-day),
	// not the same day as start. Specifically end > start by ~3 days.
	require.True(t, saveMsg.EndTime.After(saveMsg.StartTime.Add(48*time.Hour)),
		"end %v should be at least 2 days after start %v",
		saveMsg.EndTime, saveMsg.StartTime)
}
