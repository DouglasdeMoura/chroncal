package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
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
	tagsItemIdx := -1
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
		case "Tags":
			tagsItemIdx = i
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
	tagsIdx := -1
	calendarIdx := -1
	transpIdx := -1
	classIdx := -1
	peopleIdx := -1
	for i, key := range m.fieldKeys {
		switch key {
		case efKeyDescription:
			notesIdx = i
		case efKeyTags:
			tagsIdx = i
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
	assert.NotEqual(t, -1, tagsIdx)
	assert.NotEqual(t, -1, calendarIdx)
	assert.NotEqual(t, -1, transpIdx)
	assert.NotEqual(t, -1, classIdx)
	assert.NotEqual(t, -1, peopleIdx)
	assert.Equal(t, notesIdx+1, tagsIdx)
	assert.Equal(t, tagsIdx+1, calendarIdx)
	assert.Equal(t, calendarIdx+1, transpIdx)
	assert.Equal(t, transpIdx+1, classIdx)
	assert.NotEqual(t, -1, titleItemIdx)
	assert.NotEqual(t, -1, titleSeparatorIdx)
	assert.NotEqual(t, -1, repeatItemIdx)
	assert.NotEqual(t, -1, repeatSeparatorIdx)
	assert.NotEqual(t, -1, alarmsSeparatorIdx)
	assert.NotEqual(t, -1, peopleItemIdx)
	assert.NotEqual(t, -1, notesItemIdx)
	assert.NotEqual(t, -1, tagsItemIdx)
	assert.NotEqual(t, -1, calendarItemIdx)
	assert.NotEqual(t, -1, transpItemIdx)
	assert.NotEqual(t, -1, classItemIdx)
	assert.Equal(t, titleItemIdx+1, titleSeparatorIdx)
	assert.NotEqual(t, -1, alarmsItemIdx)
	assert.Greater(t, alarmsItemIdx, repeatItemIdx)
	assert.Greater(t, alarmsItemIdx, classItemIdx)
	assert.Equal(t, repeatItemIdx+1, repeatSeparatorIdx)
	assert.Equal(t, repeatSeparatorIdx+1, peopleItemIdx)
	assert.Equal(t, notesItemIdx+1, tagsItemIdx)
	assert.Equal(t, tagsItemIdx+1, calendarItemIdx)
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
	// OnSubmit emits a deferred submit marker; the model's Update then runs
	// save() against the live receiver to avoid stale captured state.
	_, ok := cmd().(eventFormSubmitNowMsg)
	require.True(t, ok, "OnSubmit must emit eventFormSubmitNowMsg")
}

func TestEventForm_EditDialogFitsSmallTerminal(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:          42,
		Title:       "Very detailed planning session",
		Description: strings.Repeat("Long notes that make the form tall.\n", 30),
		StartTime:   time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID:  1,
		Categories:  "planning, review",
	}, testEventFormCalendars(), Theme{})
	const termH = 15
	m = m.SetSize(120, termH)

	_, bh := m.BoxSize()
	require.LessOrEqual(t, bh, termH, "rendered edit form must fit inside the terminal")
	require.True(t, m.bodyOverflows(), "test precondition: form body should overflow")

	out := m.View()
	assert.Contains(t, out, "Edit Event")
	assert.Contains(t, out, "Save")
	assert.Contains(t, out, "Cancel")
	assert.Contains(t, out, "more")
}

func TestEventForm_MouseWheelScrollSurvivesRender(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         42,
		Title:      "Very detailed planning session",
		StartTime:  time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 22, 15, 0, 0, 0, time.UTC),
		CalendarID: 1,
	}, testEventFormCalendars(), Theme{})
	m = m.SetSize(120, 15)
	require.True(t, m.bodyOverflows(), "test precondition: form body should overflow")

	for range 30 {
		var cmd tea.Cmd
		m, cmd = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		require.Nil(t, cmd)
	}
	require.True(t, m.body.AtBottom(), "mouse wheel should scroll the body to the bottom")

	out := m.View()
	assert.Contains(t, out, "Alarms", "rendering must preserve the wheel-scrolled viewport")
	assert.Contains(t, out, "↑ more")
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

func TestEventForm_EditNoOpPreservesTimedStartAcrossUTCDateBoundary(t *testing.T) {
	// Event stored 2026-04-16T02:00:00Z in America/New_York. Its UTC date
	// (Apr 16) differs from its display-tz wall-clock date (Apr 15, 22:00 NY).
	// A no-op edit (just save) must not shift the event by a day.
	start := time.Date(2026, 4, 16, 2, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 16, 3, 0, 0, 0, time.UTC)
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         7,
		UID:        "boundary-uid",
		Title:      "Late meeting",
		StartTime:  start,
		EndTime:    end,
		Timezone:   "America/New_York",
		CalendarID: 1,
	}, testEventFormCalendars(), Theme{})

	cmd := m.save(&m.form)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)
	assert.True(t, msg.StartTime.Equal(start),
		"no-op edit must preserve StartTime; got %s want %s",
		msg.StartTime.Format(time.RFC3339), start.Format(time.RFC3339))
	assert.True(t, msg.EndTime.Equal(end),
		"no-op edit must preserve EndTime; got %s want %s",
		msg.EndTime.Format(time.RFC3339), end.Format(time.RFC3339))
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

// TestEventForm_EditPreservesAttendeeMetadata guards against issue #109:
// editing an event must not flatten attendee Role/RSVPStatus/CUType/CN back to
// defaults. A CHAIR who has ACCEPTED must remain a CHAIR who has ACCEPTED after
// an unrelated title edit, and unchanged attendees must keep their display name.
func TestEventForm_EditPreservesAttendeeMetadata(t *testing.T) {
	m, _ := NewEventFormModelForEdit(event.Event{
		ID:         7,
		CalendarID: 1,
		Title:      "Standup",
		Attendees: []model.Attendee{
			{
				Email:      "chair@example.com",
				Name:       "Chair Person",
				Role:       "CHAIR",
				RSVPStatus: "ACCEPTED",
				CUType:     "INDIVIDUAL",
			},
			{
				Email:      "room@example.com",
				Name:       "Big Room",
				Role:       "OPT-PARTICIPANT",
				RSVPStatus: "TENTATIVE",
				CUType:     "ROOM",
			},
		},
	}, testEventFormCalendars(), Theme{})

	m.titleField.SetValue("Standup (renamed)")

	cmd := m.save(&m.form)
	require.NotNil(t, cmd)
	msg, ok := cmd().(EventFormSaveMsg)
	require.True(t, ok)

	require.Len(t, msg.Attendees, 2)
	byEmail := map[string]model.Attendee{}
	for _, a := range msg.Attendees {
		byEmail[a.Email] = a
	}

	chair := byEmail["chair@example.com"]
	assert.Equal(t, "CHAIR", chair.Role)
	assert.Equal(t, "ACCEPTED", chair.RSVPStatus)
	assert.Equal(t, "Chair Person", chair.Name)

	room := byEmail["room@example.com"]
	assert.Equal(t, "OPT-PARTICIPANT", room.Role)
	assert.Equal(t, "TENTATIVE", room.RSVPStatus)
	assert.Equal(t, "ROOM", room.CUType)
	assert.Equal(t, "Big Room", room.Name)
}
