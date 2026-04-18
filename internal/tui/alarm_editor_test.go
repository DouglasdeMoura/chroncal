package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOffsetTrigger_RoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		qty    int
		unit   int // index into alarmUnits
		before bool
	}{
		{"minutes before", "-PT15M", 15, 0, true},
		{"hours before", "-PT2H", 2, 1, true},
		{"days before", "-P1D", 1, 2, true},
		{"weeks before", "-P2W", 2, 3, true},
		{"minutes after", "PT30M", 30, 0, false},
		{"hours after", "PT6H", 6, 1, false},
		{"days after", "P3D", 3, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qty, unit, before, ok := parseOffsetTrigger(tc.in)
			require.True(t, ok, "expected to parse %q", tc.in)
			assert.Equal(t, tc.qty, qty)
			assert.Equal(t, tc.unit, unit)
			assert.Equal(t, tc.before, before)

			rebuilt := buildOffsetTrigger(qty, unit, before)
			assert.Equal(t, tc.in, rebuilt)
		})
	}
}

func TestParseOffsetTrigger_Unsupported(t *testing.T) {
	unsupported := []string{
		"",
		"2026-04-20T09:00:00Z",
		"-P1DT12H", // mixed units
		"-PT",
		"foo",
		"-P0D", // zero
	}
	for _, s := range unsupported {
		_, _, _, ok := parseOffsetTrigger(s)
		assert.False(t, ok, "expected %q not to parse", s)
	}
}

func TestAlarmSummary(t *testing.T) {
	assert.Equal(t, "None", alarmSummary(nil))
	assert.Equal(t, "15 min. before", alarmSummary([]model.Alarm{
		{TriggerValue: "-PT15M"},
	}))
	assert.Equal(t, "3 alarms", alarmSummary([]model.Alarm{
		{TriggerValue: "-PT15M"}, {TriggerValue: "-P1D"}, {TriggerValue: "-PT1H"},
	}))
}

func TestAlarmEditor_AddEditDelete(t *testing.T) {
	m := NewAlarmListEditorModel(nil, 80, 24, Theme{})
	require.Empty(t, m.Alarms())

	// "n" enters edit mode for a brand-new alarm with the defaults
	// (15 minutes before, DISPLAY).
	m, _ = m.Update(keyMsg("n"))
	assert.Equal(t, alarmModeEdit, m.mode)
	assert.Equal(t, -1, m.editIdx)

	// ctrl+s submits the form; back in list mode with one alarm.
	m, cmd := m.Update(keyMsg("ctrl+s"))
	require.NotNil(t, cmd)
	m, _ = m.Update(cmd())
	require.Equal(t, alarmModeList, m.mode)
	require.Len(t, m.Alarms(), 1)
	assert.Equal(t, "-PT15M", m.Alarms()[0].TriggerValue)
	assert.Equal(t, "DISPLAY", m.Alarms()[0].Action)

	// "e" re-opens the same alarm for editing; ctrl+s saves unchanged.
	m.cursor = 0
	m, _ = m.Update(keyMsg("e"))
	require.Equal(t, alarmModeEdit, m.mode)
	assert.Equal(t, 0, m.editIdx)

	// Change to 1 hour before: bump unit from minutes (0) to hours (1).
	m.offsetField.SetAmount("1")
	m.offsetField.SetSelected(1)
	m, cmd = m.Update(keyMsg("ctrl+s"))
	require.NotNil(t, cmd)
	m, _ = m.Update(cmd())
	require.Equal(t, alarmModeList, m.mode)
	require.Len(t, m.Alarms(), 1)
	assert.Equal(t, "-PT1H", m.Alarms()[0].TriggerValue)

	// "d" removes the alarm.
	m.cursor = 0
	m, _ = m.Update(keyMsg("d"))
	assert.Empty(t, m.Alarms())
	assert.Equal(t, 0, m.cursor) // clamped to add-row

	// "ctrl+s" in list mode finalizes the editor.
	m, _ = m.Update(keyMsg("ctrl+s"))
	assert.True(t, m.Done())
	assert.False(t, m.Cancelled())
}

func TestAlarmEditor_EscCancels(t *testing.T) {
	m := NewAlarmListEditorModel([]model.Alarm{
		{TriggerValue: "-PT15M", Action: "DISPLAY"},
	}, 80, 24, Theme{})

	// Esc in list mode cancels the whole editor.
	m, _ = m.Update(keyMsg("esc"))
	assert.True(t, m.Cancelled())
}

func TestAlarmEditor_EscInEditModeReturnsToList(t *testing.T) {
	m := NewAlarmListEditorModel(nil, 80, 24, Theme{})
	m, _ = m.Update(keyMsg("n"))
	require.Equal(t, alarmModeEdit, m.mode)

	// Esc in edit mode goes back to the list, not cancels the editor.
	m, cmd := m.Update(keyMsg("esc"))
	require.NotNil(t, cmd)
	m, _ = m.Update(cmd())
	assert.Equal(t, alarmModeList, m.mode)
	assert.False(t, m.Cancelled())
	assert.Empty(t, m.Alarms())
}

func TestAlarmEditor_PreservesAbsoluteTrigger(t *testing.T) {
	// Absolute triggers aren't editable in this UI — they must survive a
	// round trip through the editor unchanged.
	m := NewAlarmListEditorModel([]model.Alarm{
		{TriggerValue: "2026-04-20T09:00:00Z", Action: "DISPLAY", ID: 99},
	}, 80, 24, Theme{})

	m.cursor = 0
	m, _ = m.Update(keyMsg("e"))
	// Edit is refused for non-offset triggers.
	assert.Equal(t, alarmModeList, m.mode)

	// The alarm itself is untouched.
	require.Len(t, m.Alarms(), 1)
	assert.Equal(t, "2026-04-20T09:00:00Z", m.Alarms()[0].TriggerValue)
	assert.Equal(t, int64(99), m.Alarms()[0].ID)
}

func TestAlarmEditor_NewAlarmFromNonDefaultOffset(t *testing.T) {
	m := NewAlarmListEditorModel(nil, 80, 24, Theme{})
	m, _ = m.Update(keyMsg("n"))
	require.Equal(t, alarmModeEdit, m.mode)

	// Change to 3 days before and switch action to EMAIL.
	m.offsetField.SetAmount("3")
	m.offsetField.SetSelected(2) // days
	m.actionField.SetSelected(1) // Email

	m, cmd := m.Update(keyMsg("ctrl+s"))
	require.NotNil(t, cmd)
	m, _ = m.Update(cmd())

	require.Len(t, m.Alarms(), 1)
	a := m.Alarms()[0]
	assert.Equal(t, "-P3D", a.TriggerValue)
	assert.Equal(t, "EMAIL", a.Action)
	assert.Equal(t, "START", a.Related)
}

func TestEventForm_SaveRoundTripsAlarms(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})
	m.titleField.SetValue("Deep work")
	m.alarms = []model.Alarm{{TriggerValue: "-PT15M", Action: "DISPLAY"}}

	cmd := m.save(&m.form, 0)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)
	require.Len(t, msg.Alarms, 1)
	assert.Equal(t, "-PT15M", msg.Alarms[0].TriggerValue)
}

func TestEventForm_EditPrefillsAlarms(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         11,
		Title:      "Review",
		StartTime:  time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID: 1,
		Alarms: []model.Alarm{
			{TriggerValue: "-PT30M", Action: "DISPLAY", Description: "Heads up"},
		},
	}, testEventFormCalendars(), Theme{})

	require.Len(t, m.alarms, 1)
	assert.Equal(t, "-PT30M", m.alarms[0].TriggerValue)
	assert.Equal(t, "Heads up", m.alarms[0].Description)

	require.NotNil(t, m.alarmField)
	assert.Equal(t, "30 min. before", m.alarmField.Value())
}

func TestEventForm_EnterOnAlarmsOpensEditor(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})
	alarmIdx := -1
	for i, key := range m.fieldKeys {
		if key == efKeyAlarms {
			alarmIdx = i
			break
		}
	}
	require.NotEqual(t, -1, alarmIdx)
	m.form.focused = alarmIdx

	cmd := m.tryOpenOverlay()
	assert.NotNil(t, cmd)
	assert.True(t, m.alarmEditorOpen)
}
