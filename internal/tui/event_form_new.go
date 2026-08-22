package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// NewEventFormModel creates a new event form for the given day.
func NewEventFormModel(day time.Time, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	calOpts := make([]calendarOption, 0, len(calendars))
	for id, info := range calendars {
		calOpts = append(calOpts, calendarOption{ID: id, Name: info.Name, Color: info.Color, OwnerEmail: info.OwnerEmail})
	}
	sort.Slice(calOpts, func(i, j int) bool { return calOpts[i].Name < calOpts[j].Name })

	// Default times: next half hour, 1 hour duration.
	now := time.Now()
	startHour, startMin := now.Hour(), 30
	if now.Minute() >= 30 {
		startHour++
		startMin = 0
	}
	if startHour >= 24 {
		startHour = 0
	}
	endHour := startHour + 1
	if endHour >= 24 {
		endHour -= 24
	}

	m := EventFormModel{
		day:       day,
		calendars: calOpts,
		endsDate:  day.AddDate(0, 1, 0),
		theme:     theme,
		help:      newThemedHelp(theme),
		keys: eventFormKeyMap{
			Save:  key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
			Close: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		},
	}

	// Build fields
	m.titleField = NewTextField("Event title")
	m.titleField.SetCharLimit(200)

	m.timeField = NewTimeRangeField(theme.TextDim)
	m.timeField.SetStartValue(fmt.Sprintf("%02d:%02d", startHour, startMin))
	m.timeField.SetEndValue(fmt.Sprintf("%02d:%02d", endHour, startMin))

	m.dateField = NewDatePickerField(m.day)

	m.allDayField = NewCheckboxField("All day", false)

	m.timezoneField = NewTimezoneField(LocalIANATimezone())

	m.transparencyField = NewSelectField([]SelectOption{
		{Label: "Busy", Value: "OPAQUE"},
		{Label: "Free", Value: "TRANSPARENT"},
	})

	m.visibilityField = NewSelectField([]SelectOption{
		{Label: "Public", Value: "PUBLIC"},
		{Label: "Private", Value: "PRIVATE"},
		{Label: "Confidential", Value: "CONFIDENTIAL"},
	})

	repeatOpts := make([]SelectOption, len(repeatPresets))
	for i, p := range repeatPresets {
		repeatOpts[i] = SelectOption{Label: p.Label, Value: p.Rule}
	}
	m.repeatField = NewSelectField(repeatOpts)

	endsOpts := []SelectOption{
		{Label: "Never", Value: "never"},
		{Label: "After", Value: "after"},
		{Label: "On date", Value: "ondate"},
	}
	m.endsField = NewSelectField(endsOpts)

	m.endsCountField = NewTextField("10")
	m.endsCountField.SetCharLimit(4)
	m.endsCountField.SetDigitsOnly()
	m.endsCountField.suffix = "times"
	m.endsCountField.SetValidate(validatePositiveInt)

	if len(calOpts) > 1 {
		calSelectOpts := make([]SelectOption, len(calOpts))
		calendarColors := make(map[string]string, len(calOpts))
		for i, c := range calOpts {
			label := c.Name
			if c.OwnerEmail != "" && strings.EqualFold(c.Name, c.OwnerEmail) {
				label = c.OwnerEmail
			} else if c.OwnerEmail != "" {
				label += " (" + c.OwnerEmail + ")"
			}
			calSelectOpts[i] = SelectOption{Label: label, Value: fmt.Sprintf("%d", c.ID)}
			calendarColors[calSelectOpts[i].Value] = c.Color
		}
		m.calendarField = NewSelectField(calSelectOpts)
		m.calendarField.SetRenderLabel(func(opt SelectOption, focused bool) string {
			dot := Glyphs["dot"]
			if color := calendarColors[opt.Value]; color != "" {
				dot = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(Glyphs["dot"])
			}
			name := opt.Label
			if focused {
				name = lipgloss.NewStyle().Reverse(true).Render(name)
			}
			return dot + " " + name
		})
	}

	m.peopleField = NewTextField("Comma-separated emails")
	m.peopleField.SetCharLimit(500)

	m.locationField = NewTextField("Add location")
	m.locationField.SetCharLimit(200)

	m.conferenceField = NewTextField("Add conference link")
	m.conferenceField.SetCharLimit(500)

	m.descField = NewTextAreaField("Add description")
	m.descField.SetCharLimit(500)
	m.descField.SetHeight(3)

	m.tagsField = NewTextField("Comma-separated tags")
	m.tagsField.SetCharLimit(500)

	// Build dialog + form
	m.buildDialogAndForm()

	cmd := m.form.Init()
	return m, cmd
}

// multiDayEndDate reports whether the event spans more than one calendar
// day. If so, it returns the last included day (inclusive). For all-day
// events the stored end is exclusive midnight of the day after the last
// included day. For timed events the actual end instant is used.
//
// Timed events are evaluated in the event's display timezone. That is the
// same loc NewEventFormModelForEditInstance uses to anchor m.day and render
// the Time field, not machine-local. Machine-local here would disagree with
// the start day on the last calendar day when ev.Timezone != local and the
// end instant is near midnight. The saved end would then shift forward a
// day on a no-op edit (issue #499).
func multiDayEndDate(ev event.Event) (time.Time, bool) {
	if ev.AllDay {
		s := ev.StartTime.UTC()
		e := ev.EndTime.UTC()
		startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.UTC)
		// End is exclusive: subtract a minute to get the last included day.
		last := e.Add(-time.Minute)
		lastDay := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
		if lastDay.After(startDay) {
			return lastDay, true
		}
		return time.Time{}, false
	}
	displayLoc := time.Local
	if ev.Timezone != "" {
		if loc, err := time.LoadLocation(ev.Timezone); err == nil {
			displayLoc = loc
		}
	}
	s := ev.StartTime.In(displayLoc)
	e := ev.EndTime.In(displayLoc)
	startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, s.Location())
	endDay := time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, e.Location())
	// If the end instant is exactly midnight of the next day, the event
	// doesn't actually touch that day (exclusive semantics).
	if e.Equal(endDay) && !endDay.Equal(startDay) {
		endDay = endDay.AddDate(0, 0, -1)
	}
	if endDay.After(startDay) {
		return endDay, true
	}
	return time.Time{}, false
}

// NewEventFormModelForEdit creates a form pre-filled with an event's stored data.
func NewEventFormModelForEdit(ev event.Event, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	return NewEventFormModelForEditInstance(ev, time.Time{}, calendars, theme)
}

// NewEventFormModelForEditInstance is like NewEventFormModelForEdit but also
// records the instance time the user clicked on for a recurring event. The
// instance time travels with the form so the parent can prompt for scope
// (this event / this and following / all events) on save.
func NewEventFormModelForEditInstance(ev event.Event, instanceTime time.Time, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	m, cmd := NewEventFormModel(ev.StartTime, calendars, theme)
	m.editID = ev.ID
	m.instanceTime = instanceTime
	m.titleField.SetValue(ev.Title)
	m.locationField.SetValue(ev.Location)
	m.conferenceField.SetValue(ev.ConferenceURI)
	m.descField.SetValue(ev.Description)

	// Restore timezone.
	if ev.Timezone != "" {
		m.timezoneField.SetValue(ev.Timezone)
	}
	if ev.Transp != "" {
		for i, opt := range m.transparencyField.options {
			if strings.EqualFold(opt.Value, ev.Transp) {
				m.transparencyField.SetSelected(i)
				break
			}
		}
	}
	if ev.Class != "" {
		for i, opt := range m.visibilityField.options {
			if strings.EqualFold(opt.Value, ev.Class) {
				m.visibilityField.SetSelected(i)
				break
			}
		}
	}

	// Resolve the display timezone for formatting start/end times.
	displayLoc := time.Local
	if ev.Timezone != "" {
		if loc, err := time.LoadLocation(ev.Timezone); err == nil {
			displayLoc = loc
		}
	}

	if ev.AllDay {
		m.allDayField.SetChecked(true)
		m.allDay = true
	}
	if !ev.AllDay {
		// Anchor the Date field to the same wall-clock day as the Time
		// field. NewEventFormModel seeded m.day from the raw (UTC)
		// StartTime, but the Time field renders in displayLoc; if the
		// event's UTC date differs from its display-tz date, save() would
		// recombine a mismatched date and time and shift the event by a
		// day. Re-anchoring m.day to displayLoc keeps both columns on the
		// same day (see issue #91).
		start := ev.StartTime.In(displayLoc)
		m.day = start
		m.timeField.SetStartValue(start.Format("15:04"))
		m.timeField.SetEndValue(ev.EndTime.In(displayLoc).Format("15:04"))
	}

	// Detect multi-day events and pre-fill the range. For all-day events
	// the stored end is exclusive midnight of the day after the last
	// included day; for timed events it's the actual end instant.
	if endDate, ok := multiDayEndDate(ev); ok {
		m.rangeHasEnd = true
		m.rangeEndDate = endDate
		m.dateField.SetRangeEnd(endDate)
	}

	// Select the correct calendar.
	if m.calendarField != nil {
		for i, c := range m.calendars {
			if c.ID == ev.CalendarID {
				m.calendarField.SetSelected(i)
				m.calendarIdx = i
				break
			}
		}
	}

	// Pre-fill attendees.
	if len(ev.Attendees) > 0 {
		m.origAttendees = append([]model.Attendee(nil), ev.Attendees...)
		emails := make([]string, 0, len(ev.Attendees))
		for _, a := range ev.Attendees {
			emails = append(emails, a.Email)
		}
		m.peopleField.SetValue(strings.Join(emails, ", "))
	}

	if ev.Categories != "" {
		m.tagsField.SetValue(ev.Categories)
	}

	// Pre-fill alarms.
	if len(ev.Alarms) > 0 {
		m.alarms = append([]model.Alarm(nil), ev.Alarms...)
	}

	// Parse recurrence rule.
	if ev.RecurrenceRule != "" {
		m.repeatIdx, m.customRule, m.ends, m.endsDate = parseRecurrenceRule(ev.RecurrenceRule, m.day)
		m.repeatField.SetSelected(m.repeatIdx)
		m.endsField.SetSelected(int(m.ends))
		if m.ends == endsAfter {
			if count := rruleParam(ev.RecurrenceRule, "COUNT"); count != "" {
				m.endsCountField.SetValue(count)
			}
		}
	}

	m.rebuildDialog()
	return m, cmd
}

// NewEventFormModelForDuplicate creates a form pre-filled with a stored
// event's data but in create mode (editID = 0).
func NewEventFormModelForDuplicate(ev event.Event, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	m, cmd := NewEventFormModelForEdit(ev, calendars, theme)
	m.editID = 0
	// A duplicate is a new local event. Keep only the fireable alarms:
	// a preserved sync-only VALARM belongs to another client, and a copy
	// would publish that sentinel under a fresh UID (issue #579).
	kept := m.alarms[:0:0]
	for _, a := range m.alarms {
		if model.FireableAlarmAction(a.Action) {
			kept = append(kept, a)
		}
	}
	m.alarms = kept
	m.rebuildDialog()
	return m, cmd
}
