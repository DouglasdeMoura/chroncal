package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defaultCalendarFixture holds three writable calendars. "Work" is the
// default, but "Personal" sorts first by name (issue #829).
func defaultCalendarFixture() map[int64]CalendarInfo {
	return map[int64]CalendarInfo{
		1: {Name: "Work", IsDefault: true},
		2: {Name: "Personal"},
		3: {Name: "Shared"},
	}
}

func savedCalendarID(t *testing.T, m EventFormModel) int64 {
	t.Helper()
	m.titleField.SetValue("Planning")
	cmd := m.save(&m.form)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)
	return msg.CalendarID
}

func TestEventForm_NewPreselectsGivenCalendar(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), defaultCalendarFixture(), Theme{}, 1)
	assert.Equal(t, int64(1), savedCalendarID(t, m))
}

func TestEventForm_NewWithoutPreselectSelectsFirstByName(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), defaultCalendarFixture(), Theme{})
	assert.Equal(t, int64(2), savedCalendarID(t, m))
}

func TestEventForm_DuplicateKeepsSourceCalendar(t *testing.T) {
	ev := event.Event{
		CalendarID: 3,
		Title:      "Sync",
		StartTime:  time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC),
	}
	m, _ := NewEventFormModelForDuplicate(ev, defaultCalendarFixture(), Theme{}, 1)
	assert.Equal(t, int64(3), savedCalendarID(t, m))
}

func TestEventForm_DuplicateFromUnlistedCalendarUsesDefault(t *testing.T) {
	// The source calendar is read-only, so eventFormCalendars drops it.
	ev := event.Event{
		CalendarID: 99,
		Title:      "Sync",
		StartTime:  time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC),
	}
	m, _ := NewEventFormModelForDuplicate(ev, defaultCalendarFixture(), Theme{}, 1)
	assert.Equal(t, int64(1), savedCalendarID(t, m))
}

func openCreateForm(t *testing.T, m Model) EventFormModel {
	t.Helper()
	m.theme = NewTheme(true)
	m.width, m.height = 120, 40
	next, _ := m.Update(EventCreateMsg{Day: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)})
	got, ok := next.(Model)
	require.True(t, ok)
	require.True(t, got.formOpen)
	return got.form
}

func TestEventCreateMsg_PreselectsDefaultCalendar(t *testing.T) {
	form := openCreateForm(t, Model{calendars: defaultCalendarFixture()})
	assert.Equal(t, int64(1), savedCalendarID(t, form))
}

func TestEventCreateMsg_HiddenDefaultFallsBackToFirstByName(t *testing.T) {
	form := openCreateForm(t, Model{
		calendars:       defaultCalendarFixture(),
		hiddenCalendars: map[int64]bool{1: true},
	})
	assert.Equal(t, int64(2), savedCalendarID(t, form))
}

func TestEventCreateMsg_ReadOnlyDefaultFallsBackToFirstByName(t *testing.T) {
	cals := defaultCalendarFixture()
	info := cals[1]
	info.RemoteAccess = "read"
	cals[1] = info
	form := openCreateForm(t, Model{calendars: cals})
	assert.Equal(t, int64(2), savedCalendarID(t, form))
}
