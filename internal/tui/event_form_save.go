package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/model"
)

func (m EventFormModel) buildRecurrenceRule() string {
	// Read straight from the pointer-backed fields so we don't depend on
	// the cached m.repeatIdx / m.ends, which are populated by syncFromForm
	// on the form's captured receiver — not on every value copy of the
	// model that reaches save().
	repeatIdx := m.repeatField.Selected()
	if repeatIdx == 0 {
		return ""
	}
	if repeatIdx == repeatCustomIdx {
		return m.customRule
	}
	rule := repeatPresets[repeatIdx].Rule
	switch endsMode(m.endsField.Selected()) {
	case endsAfter:
		count := strings.TrimSpace(m.endsCountField.Value())
		if count != "" {
			rule += ";COUNT=" + count
		}
	case endsOnDate:
		rule += ";UNTIL=" + formatRRuleUntil(m.endsDate)
	default:
		// endsNever
	}
	return rule
}

func (m EventFormModel) save(f *Form) tea.Cmd {
	calIdx := 0
	if m.calendarField != nil {
		calIdx = m.calendarField.Selected()
	}
	if calIdx >= len(m.calendars) || len(m.calendars) == 0 {
		f.SetError(0, "No calendars available")
		return nil
	}
	calID := m.calendars[calIdx].ID
	day := m.day
	rrule := m.buildRecurrenceRule()
	title := strings.TrimSpace(m.titleField.Value())
	tzName := m.timezoneField.Value()

	// Parse comma-separated emails into attendees. The People field only
	// exposes email addresses, so re-attach each email's original participation
	// metadata (Role/RSVPStatus/CUType/CN, etc.) instead of overwriting it with
	// defaults. New emails get the standard defaults (issue #109).
	orig := make(map[string]model.Attendee, len(m.origAttendees))
	for _, a := range m.origAttendees {
		orig[strings.ToLower(a.Email)] = a
	}
	var attendees []model.Attendee
	if raw := strings.TrimSpace(m.peopleField.Value()); raw != "" {
		seen := make(map[string]bool)
		for _, part := range strings.Split(raw, ",") {
			email := strings.TrimSpace(part)
			if email == "" {
				continue
			}
			key := strings.ToLower(email)
			if seen[key] {
				continue
			}
			seen[key] = true
			if prev, ok := orig[key]; ok {
				attendees = append(attendees, prev)
				continue
			}
			attendees = append(attendees, model.Attendee{
				Email:      email,
				Role:       "REQ-PARTICIPANT",
				RSVPStatus: "NEEDS-ACTION",
				CUType:     "INDIVIDUAL",
			})
		}
	}

	// Resolve the time.Location for the selected timezone.
	loc := time.UTC
	if tzName != "" && tzName != "UTC" {
		if parsed, err := time.LoadLocation(tzName); err == nil {
			loc = parsed
		}
	}

	if m.allDayField.Checked() {
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 0, 1)
		if m.rangeHasEnd {
			endDay := time.Date(m.rangeEndDate.Year(), m.rangeEndDate.Month(), m.rangeEndDate.Day(), 0, 0, 0, 0, time.UTC)
			end = endDay.AddDate(0, 0, 1)
		}
		return func() tea.Msg {
			return EventFormSaveMsg{
				CalendarID:     calID,
				Title:          title,
				Description:    strings.TrimSpace(m.descField.Value()),
				Location:       strings.TrimSpace(m.locationField.Value()),
				ConferenceURI:  strings.TrimSpace(m.conferenceField.Value()),
				StartTime:      start,
				EndTime:        end,
				AllDay:         true,
				RecurrenceRule: rrule,
				Timezone:       tzName,
				Transp:         m.transparencyField.Value(),
				Class:          m.visibilityField.Value(),
				Categories:     strings.TrimSpace(m.tagsField.Value()),
				Attendees:      attendees,
				Alarms:         m.alarms,
				InstanceTime:   m.instanceTime,
			}
		}
	}

	startVal := strings.TrimSpace(m.timeField.StartValue())
	endVal := strings.TrimSpace(m.timeField.EndValue())

	st, err := time.Parse("15:04", startVal)
	if err != nil {
		for i, k := range m.fieldKeys {
			if k == efKeyTime {
				f.SetError(i, "Invalid start time (use HH:MM)")
				return nil
			}
		}
		return nil
	}
	et, err := time.Parse("15:04", endVal)
	if err != nil {
		for i, k := range m.fieldKeys {
			if k == efKeyTime {
				f.SetError(i, "Invalid end time (use HH:MM)")
				return nil
			}
		}
		return nil
	}

	// Interpret entered times in the selected timezone, then convert to UTC.
	start := time.Date(day.Year(), day.Month(), day.Day(),
		st.Hour(), st.Minute(), 0, 0, loc).UTC()
	endDay := day
	if m.rangeHasEnd {
		endDay = m.rangeEndDate
		// rangeEndDate holds the last *included* day (multiDayEndDate
		// subtracts the exclusive midnight day for display). A midnight
		// end time means the event runs through the end of that day, i.e.
		// to exclusive midnight of the following day, so re-add the day
		// here to keep the save path symmetric with the display path and
		// avoid silently shifting the end back 24h (issue #208). Mirrors
		// the all-day branch's endDay.AddDate(0, 0, 1).
		if et.Hour() == 0 && et.Minute() == 0 {
			endDay = endDay.AddDate(0, 0, 1)
		}
	}
	end := time.Date(endDay.Year(), endDay.Month(), endDay.Day(),
		et.Hour(), et.Minute(), 0, 0, loc).UTC()
	// Same-day cross-midnight fallback (HH:MM→HH:MM where end ≤ start).
	if !m.rangeHasEnd && !end.After(start) {
		end = end.AddDate(0, 0, 1)
	}

	desc := strings.TrimSpace(m.descField.Value())
	location := strings.TrimSpace(m.locationField.Value())
	conference := strings.TrimSpace(m.conferenceField.Value())

	return func() tea.Msg {
		return EventFormSaveMsg{
			CalendarID:     calID,
			Title:          title,
			Description:    desc,
			Location:       location,
			ConferenceURI:  conference,
			StartTime:      start,
			EndTime:        end,
			RecurrenceRule: rrule,
			Timezone:       tzName,
			Transp:         m.transparencyField.Value(),
			Class:          m.visibilityField.Value(),
			Categories:     strings.TrimSpace(m.tagsField.Value()),
			Attendees:      attendees,
			Alarms:         m.alarms,
			InstanceTime:   m.instanceTime,
		}
	}
}
