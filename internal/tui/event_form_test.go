package tui

import (
	"strings"
	"testing"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
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

	notesIdx := -1
	calendarIdx := -1
	for i, key := range m.fieldKeys {
		switch key {
		case efKeyDescription:
			notesIdx = i
		case efKeyCalendar:
			calendarIdx = i
		}
	}

	assert.NotEqual(t, -1, notesIdx)
	assert.NotEqual(t, -1, calendarIdx)
	assert.Equal(t, notesIdx+1, calendarIdx)
	assert.Equal(t, "Notes", m.form.items[notesIdx].Label)
	assert.Equal(t, "Calendar", m.form.items[calendarIdx].Label)
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
