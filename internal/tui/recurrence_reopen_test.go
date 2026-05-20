package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/stretchr/testify/require"
)

// Regression: when re-opening the recurrence editor on an event that already
// has a custom weekly rule (e.g. Mon-Thu), the editor must restore the
// existing weekday selections. Otherwise confirming with Enter/Ctrl+S
// silently overwrites the saved rule with just the start day, which is what
// the user reported as "only saved the event on Wed and Thu".
func TestRecurrenceEditor_ReopenRestoresExistingWeeklyRule(t *testing.T) {
	day := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC) // Wednesday
	ev := event.Event{
		ID:             42,
		Title:          "Standup",
		StartTime:      day,
		EndTime:        day.Add(30 * time.Minute),
		CalendarID:     1,
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH",
	}

	m, _ := NewEventFormModelForEdit(ev, testEventFormCalendars(), Theme{})
	m = m.SetSize(120, 40)

	require.Equal(t, "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH", m.customRule,
		"parseRecurrenceRule should have stored the custom rule on the form")

	repeatIdx := -1
	for i, key := range m.fieldKeys {
		if key == efKeyRepeat {
			repeatIdx = i
			break
		}
	}
	require.NotEqual(t, -1, repeatIdx)
	m.form.focused = repeatIdx

	// Open the editor the same way the user does: Enter on the Repeat field
	// when it's already on "Custom...".
	require.Equal(t, repeatCustomIdx, m.repeatField.Selected())
	m, _ = m.Update(keyPressMsg("enter"))
	require.True(t, m.rruleEditorOpen)

	// The editor must reflect what the user previously chose: Mo, Tu, We, Th.
	wd := m.rruleEditor.onField.WeekDays()
	require.True(t, wd[1], "Mo should be restored")
	require.True(t, wd[2], "Tu should be restored")
	require.True(t, wd[3], "We should be restored")
	require.True(t, wd[4], "Th should be restored")
	require.False(t, wd[0] || wd[5] || wd[6],
		"Su/Fr/Sa should not be selected: got %v", wd)

	// Confirm with Ctrl+S without changing anything — the saved rule must
	// still be the four-day BYDAY, not a degenerate single-day rule.
	m, _ = m.Update(keyPressMsg("ctrl+s"))
	m, _ = m.Update(recurrenceEditorDone)
	require.False(t, m.rruleEditorOpen)
	require.Equal(t, "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH", m.customRule)
}

func TestRecurrenceEditor_LoadRuleRestoresIntervalCountUntilMonthly(t *testing.T) {
	day := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC) // Wednesday

	t.Run("interval+count", func(t *testing.T) {
		m := NewRecurrenceEditorModel(day, 120, 40, Theme{})
		m.LoadRule("FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;COUNT=10")
		require.Equal(t, "WEEKLY", m.currentFreq())
		require.Equal(t, 2, m.intervalValue())
		require.Equal(t, endsAfter, m.currentEnds())
		require.Equal(t, "10", m.endsCountField.Value())
		require.Equal(t, "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;COUNT=10", m.BuildRule())
	})

	t.Run("until", func(t *testing.T) {
		m := NewRecurrenceEditorModel(day, 120, 40, Theme{})
		m.LoadRule("FREQ=DAILY;UNTIL=20260620T000000Z")
		require.Equal(t, "DAILY", m.currentFreq())
		require.Equal(t, endsOnDate, m.currentEnds())
		require.Equal(t, "FREQ=DAILY;UNTIL=20260620T000000Z", m.BuildRule())
	})

	t.Run("monthly nth weekday", func(t *testing.T) {
		m := NewRecurrenceEditorModel(day, 120, 40, Theme{})
		m.LoadRule("FREQ=MONTHLY;BYDAY=3WE")
		require.Equal(t, "MONTHLY", m.currentFreq())
		require.Equal(t, 1, m.onField.MonthlyMode())
		require.Equal(t, "FREQ=MONTHLY;BYDAY=3WE", m.BuildRule())
	})
}
