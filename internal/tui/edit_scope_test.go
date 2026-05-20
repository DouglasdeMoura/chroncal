package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/stretchr/testify/require"
)

// EventFormSaveMsg from a recurring-instance edit must carry the original
// instance time so the app loop can route to the right scope handler.
// Without this, every save would silently fall through to "all events".
func TestEventForm_RecurringInstanceSavePopulatesInstanceTime(t *testing.T) {
	day := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC) // Wednesday
	calendars := testEventFormCalendars()

	// Open the form as if the user clicked on a single instance of a weekly
	// series. The clicked time is `day` itself — the master might have a
	// different DTSTART but for this test it doesn't matter.
	m, _ := NewEventFormModelForEditInstance(
		stubRecurringEvent(day),
		day,
		calendars,
		Theme{},
	)
	m = m.SetSize(120, 40)

	require.Equal(t, day, m.instanceTime,
		"form must remember the clicked instance time")

	// Submit; the resulting EventFormSaveMsg must echo the instance time so
	// app.go can pop the scope dialog and route to UpdateInstance etc.
	_, cmd := m.Update(keyPressMsg("ctrl+s"))
	require.NotNil(t, cmd)
	first := cmd()
	if _, ok := first.(eventFormSubmitNowMsg); ok {
		_, cmd = m.Update(first)
		require.NotNil(t, cmd)
		first = cmd()
	}
	save, ok := first.(EventFormSaveMsg)
	require.True(t, ok, "expected EventFormSaveMsg, got %T", first)
	require.Equal(t, day, save.InstanceTime,
		"save msg must carry the clicked instance time")
}

// Sanity check: opening the form via the non-instance constructor leaves
// InstanceTime zero, so non-recurring edits keep their existing path.
func TestEventForm_NonInstanceSaveLeavesInstanceTimeZero(t *testing.T) {
	day := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)
	m, _ := NewEventFormModelForEdit(stubRecurringEvent(day), testEventFormCalendars(), Theme{})
	m = m.SetSize(120, 40)

	require.True(t, m.instanceTime.IsZero())

	_, cmd := m.Update(keyPressMsg("ctrl+s"))
	require.NotNil(t, cmd)
	first := cmd()
	if _, ok := first.(eventFormSubmitNowMsg); ok {
		_, cmd = m.Update(first)
		first = cmd()
	}
	save, ok := first.(EventFormSaveMsg)
	require.True(t, ok)
	require.True(t, save.InstanceTime.IsZero(),
		"non-instance form must not synthesise an InstanceTime")
}

func stubRecurringEvent(day time.Time) event.Event {
	return event.Event{
		ID:             42,
		Title:          "Standup",
		StartTime:      day,
		EndTime:        day.Add(30 * time.Minute),
		CalendarID:     1,
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH",
	}
}
