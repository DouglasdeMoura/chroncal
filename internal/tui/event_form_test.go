package tui

import (
	"strings"
	"testing"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEventFormCalendars() map[int64]CalendarInfo {
	return map[int64]CalendarInfo{
		1: {Name: "Work", Color: "#ff0000", OwnerEmail: "work@example.com"},
		2: {Name: "Personal", Color: "#00ff00"},
		3: {Name: "same@example.com", Color: "#0000ff", OwnerEmail: "same@example.com"},
	}
}

func TestEventForm_PlacesCalendarAfterNotes(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	repeatItemIdx := -1
	alarmsItemIdx := -1
	titleItemIdx := -1
	titleSeparatorIdx := -1
	repeatSeparatorIdx := -1
	alarmsSeparatorIdx := -1
	peopleItemIdx := -1
	notesItemIdx := -1
	calendarItemIdx := -1
	transpItemIdx := -1
	classItemIdx := -1
	sepCount := 0
	for i, item := range m.form.items {
		switch item.Label {
		case "Title":
			titleItemIdx = i
		case "Repeat":
			repeatItemIdx = i
		case "Alarms":
			alarmsItemIdx = i
		case "People":
			peopleItemIdx = i
		case "Notes":
			notesItemIdx = i
		case "Calendar":
			calendarItemIdx = i
		case "Show as":
			transpItemIdx = i
		case "Visibility":
			classItemIdx = i
		case "":
			if _, ok := item.Field.(*StaticField); ok {
				switch sepCount {
				case 0:
					titleSeparatorIdx = i
				case 1:
					repeatSeparatorIdx = i
				case 2:
					alarmsSeparatorIdx = i
				}
				sepCount++
			}
		}
	}

	notesIdx := -1
	calendarIdx := -1
	transpIdx := -1
	classIdx := -1
	peopleIdx := -1
	for i, key := range m.fieldKeys {
		switch key {
		case efKeyDescription:
			notesIdx = i
		case efKeyCalendar:
			calendarIdx = i
		case efKeyTransp:
			transpIdx = i
		case efKeyClass:
			classIdx = i
		case efKeyPeople:
			peopleIdx = i
		}
	}

	assert.NotEqual(t, -1, notesIdx)
	assert.NotEqual(t, -1, calendarIdx)
	assert.NotEqual(t, -1, transpIdx)
	assert.NotEqual(t, -1, classIdx)
	assert.NotEqual(t, -1, peopleIdx)
	assert.Equal(t, notesIdx+1, calendarIdx)
	assert.Equal(t, calendarIdx+1, transpIdx)
	assert.Equal(t, transpIdx+1, classIdx)
	assert.NotEqual(t, -1, titleItemIdx)
	assert.NotEqual(t, -1, titleSeparatorIdx)
	assert.NotEqual(t, -1, repeatItemIdx)
	assert.NotEqual(t, -1, repeatSeparatorIdx)
	assert.NotEqual(t, -1, alarmsSeparatorIdx)
	assert.NotEqual(t, -1, peopleItemIdx)
	assert.NotEqual(t, -1, notesItemIdx)
	assert.NotEqual(t, -1, calendarItemIdx)
	assert.NotEqual(t, -1, transpItemIdx)
	assert.NotEqual(t, -1, classItemIdx)
	assert.Equal(t, titleItemIdx+1, titleSeparatorIdx)
	assert.NotEqual(t, -1, alarmsItemIdx)
	assert.Greater(t, alarmsItemIdx, repeatItemIdx)
	assert.Greater(t, alarmsItemIdx, classItemIdx)
	assert.Equal(t, repeatItemIdx+1, repeatSeparatorIdx)
	assert.Equal(t, repeatSeparatorIdx+1, peopleItemIdx)
	assert.Equal(t, notesItemIdx+1, calendarItemIdx)
	assert.Equal(t, calendarItemIdx+1, transpItemIdx)
	assert.Equal(t, transpItemIdx+1, classItemIdx)
	assert.Equal(t, classItemIdx+1, alarmsSeparatorIdx)
	assert.Equal(t, alarmsSeparatorIdx+1, alarmsItemIdx)
	assert.Equal(t, len(m.form.items)-1, alarmsItemIdx)
}

func TestEventForm_CalendarFieldRendersColorDot(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	if assert.NotNil(t, m.calendarField) {
		m.calendarField.SetSelected(1)
		view := m.calendarField.View()
		assert.Contains(t, view, Glyphs["dot"])
		assert.Contains(t, view, "Work (work@example.com)")
	}
}

func TestEventForm_CalendarFieldOmitsEmailWhenAbsent(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	if assert.NotNil(t, m.calendarField) {
		m.calendarField.SetSelected(0)
		view := m.calendarField.View()
		assert.Contains(t, view, "Personal")
		assert.False(t, strings.Contains(view, "("))
	}
}

func TestEventForm_CalendarFieldShowsOnlyEmailWhenNameMatches(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	if assert.NotNil(t, m.calendarField) {
		m.calendarField.SetSelected(2)
		view := m.calendarField.View()
		assert.Contains(t, view, "same@example.com")
		assert.False(t, strings.Contains(view, "same@example.com ("))
	}
}

func TestEventForm_CalendarFieldFocusBackgroundAppliesOnlyToName(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	if assert.NotNil(t, m.calendarField) {
		m.calendarField.SetSelected(1)
		m.calendarField.Focus()

		view := m.calendarField.View()
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render(Glyphs["dot"])
		name := lipgloss.NewStyle().Reverse(true).Render("Work (work@example.com)")

		assert.Contains(t, view, dot+" "+name)
	}
}

func TestEventForm_DefaultsShowBusyAndPublic(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	require.NotNil(t, m.transparencyField)
	require.NotNil(t, m.visibilityField)
	assert.Equal(t, "OPAQUE", m.transparencyField.Value())
	assert.Equal(t, "PUBLIC", m.visibilityField.Value())
}

func TestEventForm_EditHydratesShowAsAndVisibility(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         7,
		Title:      "Review",
		StartTime:  time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID: 1,
		Transp:     "TRANSPARENT",
		Class:      "PRIVATE",
	}, testEventFormCalendars(), Theme{})

	require.NotNil(t, m.transparencyField)
	require.NotNil(t, m.visibilityField)
	assert.Equal(t, "TRANSPARENT", m.transparencyField.Value())
	assert.Equal(t, "PRIVATE", m.visibilityField.Value())
}

// TestEventForm_EditModeRetainsEditID is a regression test: after building
// the form via NewEventFormModelForEdit, the live form model must carry the
// edited event's ID so the app handler can dispatch an Update. Previously
// the OnSubmit closure captured editID=0 (from the create-mode constructor)
// and sent it through EventFormSaveMsg; the parent then fell into the Create
// branch and created a duplicate event with a fresh UID.
func TestEventForm_EditModeRetainsEditID(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         42,
		UID:        "original-uid",
		Title:      "Pay the bills!!!",
		StartTime:  time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID: 1,
	}, testEventFormCalendars(), Theme{})

	assert.Equal(t, int64(42), m.editID, "NewEventFormModelForEdit must populate editID on the returned form")

	require.NotNil(t, m.form.onSubmit, "edit form must have an OnSubmit callback")
	cmd := m.form.onSubmit(&m.form)
	require.NotNil(t, cmd)
	_, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok, "OnSubmit must emit EventFormSaveMsg")
}

func TestEventForm_SaveIncludesShowAsAndVisibility(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})
	m.titleField.SetValue("Planning")
	m.transparencyField.SetSelected(1)
	m.visibilityField.SetSelected(2)

	cmd := m.save(&m.form)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)
	assert.Equal(t, "TRANSPARENT", msg.Transp)
	assert.Equal(t, "CONFIDENTIAL", msg.Class)
}

func TestEventForm_EnterOnTimeDoesNotOpenDatePicker(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	timeIdx := -1
	for i, item := range m.form.items {
		if item.Label == "Time" {
			timeIdx = i
			break
		}
	}
	require.NotEqual(t, -1, timeIdx)
	m.form.focused = timeIdx

	cmd := m.tryOpenOverlay()
	assert.Nil(t, cmd)
	assert.False(t, m.datePickerOpen)
	assert.False(t, m.timezonePickerOpen)
}

func TestEventForm_EnterOnDateOpensDatePicker(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	dateIdx := -1
	for i, item := range m.form.items {
		if item.Label == "Date" {
			dateIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dateIdx)
	m.form.focused = dateIdx

	cmd := m.tryOpenOverlay()
	require.NotNil(t, cmd)
	assert.True(t, m.datePickerOpen)
	assert.False(t, m.timezonePickerOpen)
}

func TestEventForm_EnterOnAllDayDoesNotOpenTimezonePicker(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	allDayIdx := -1
	for i, item := range m.form.items {
		if item.Label == "All day" {
			allDayIdx = i
			break
		}
	}
	require.NotEqual(t, -1, allDayIdx)
	m.form.focused = allDayIdx

	cmd := m.tryOpenOverlay()
	assert.Nil(t, cmd)
	assert.False(t, m.datePickerOpen)
	assert.False(t, m.timezonePickerOpen)
}

func TestEventForm_EnterOnAllDayDisablesTimeImmediately(t *testing.T) {
	m, _ := NewEventFormModel(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), testEventFormCalendars(), Theme{})

	allDayIdx := -1
	for i, item := range m.form.items {
		if item.Label == "All day" {
			allDayIdx = i
			break
		}
	}
	require.NotEqual(t, -1, allDayIdx)
	require.True(t, m.timeField.IsFocusable())

	m.form.focused = allDayIdx
	m, _ = m.Update(keyPressMsg("enter"))

	assert.True(t, m.allDayField.Checked())
	assert.False(t, m.timeField.IsFocusable())
}
