package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// User flow: create event on Wed 2026-05-20, open recurrence editor, navigate
// to "On" picker, toggle Mo, Tu, We, Th via arrow keys + space, then submit.
func TestRecurrenceEditor_UserKeysProduceMonThuRule(t *testing.T) {
	day := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC) // Wednesday
	m := NewRecurrenceEditorModel(day, 120, 40, Theme{})

	tabUntilOn := func() {
		for i := 0; i < 10; i++ {
			if m.form.Focused() < len(m.form.items) {
				if _, ok := m.form.items[m.form.Focused()].Field.(*RecurrenceOnField); ok {
					return
				}
			}
			m, _ = m.Update(keyPressMsg("tab"))
		}
		t.Fatal("never reached RecurrenceOnField")
	}
	tabUntilOn()

	// Initial state: Wed pre-selected (cursor at Wed=3).
	require.True(t, m.onField.WeekDays()[3], "Wed should be pre-selected")
	require.Equal(t, 3, m.onField.WeekDayCursor())

	// Navigate LEFT to Mon (cursor 3→2→1) and toggle.
	m, _ = m.Update(keyPressMsg("left"))
	m, _ = m.Update(keyPressMsg("left"))
	require.Equal(t, 1, m.onField.WeekDayCursor(), "cursor should be on Mo")
	m, _ = m.Update(keyPressMsg("space"))
	require.True(t, m.onField.WeekDays()[1], "Mo should be selected after space")

	// Navigate RIGHT to Tu (cursor 1→2) and toggle.
	m, _ = m.Update(keyPressMsg("right"))
	require.Equal(t, 2, m.onField.WeekDayCursor())
	m, _ = m.Update(keyPressMsg("space"))
	require.True(t, m.onField.WeekDays()[2], "Tu should be selected")

	// Navigate RIGHT past Wed (already on) to Thu (cursor 2→3→4) and toggle.
	m, _ = m.Update(keyPressMsg("right"))
	m, _ = m.Update(keyPressMsg("right"))
	require.Equal(t, 4, m.onField.WeekDayCursor())
	m, _ = m.Update(keyPressMsg("space"))
	require.True(t, m.onField.WeekDays()[4], "Th should be selected")

	// Final state: Mo, Tu, We, Th selected.
	wd := m.onField.WeekDays()
	require.True(t, wd[1] && wd[2] && wd[3] && wd[4],
		"Mo,Tu,We,Th should all be selected: got %v", wd)
	require.False(t, wd[0] || wd[5] || wd[6],
		"Su,Fr,Sa should not be selected: got %v", wd)

	require.Equal(t, "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH", m.BuildRule())
}
