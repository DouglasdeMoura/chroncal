package ical

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
)

func ImportFile(r io.Reader) ([]event.Event, error) {
	dec := ical.NewDecoder(r)
	var events []event.Event

	for {
		cal, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode ical: %w", err)
		}

		for _, child := range cal.Children {
			if child.Name != ical.CompEvent {
				continue
			}
			vevent := ical.Event{Component: child}
			e, err := eventFromVEvent(vevent)
			if err != nil {
				continue
			}
			events = append(events, e)
		}
	}

	return events, nil
}

func eventFromVEvent(ve ical.Event) (event.Event, error) {
	uid, err := ve.Props.Text(ical.PropUID)
	if err != nil || uid == "" {
		return event.Event{}, fmt.Errorf("missing UID")
	}

	summary, _ := ve.Props.Text(ical.PropSummary)
	description, _ := ve.Props.Text(ical.PropDescription)
	location, _ := ve.Props.Text(ical.PropLocation)

	// Timezone from DTSTART param
	var timezone string
	if prop := ve.Props.Get(ical.PropDateTimeStart); prop != nil {
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			timezone = tzid
		}
	}

	startTime, err := ve.Props.DateTime(ical.PropDateTimeStart, nil)
	if err != nil {
		return event.Event{}, fmt.Errorf("parse DTSTART: %w", err)
	}

	endTime, err := ve.Props.DateTime(ical.PropDateTimeEnd, nil)
	if err != nil {
		endTime = startTime.Add(time.Hour)
	}

	allDay := false
	if prop := ve.Props.Get(ical.PropDateTimeStart); prop != nil {
		if strings.EqualFold(prop.Params.Get("VALUE"), "DATE") {
			allDay = true
		}
	}

	rrule, _ := ve.Props.Text(ical.PropRecurrenceRule)

	// RFC 5545 properties
	status := textOrDefault(ve, ical.PropStatus, "CONFIRMED")
	transp := textOrDefault(ve, ical.PropTransparency, "OPAQUE")
	class := textOrDefault(ve, ical.PropClass, "PUBLIC")

	var sequence int64
	if prop := ve.Props.Get("SEQUENCE"); prop != nil {
		if v, err := strconv.ParseInt(prop.Value, 10, 64); err == nil {
			sequence = v
		}
	}

	var priority int64
	if prop := ve.Props.Get(ical.PropPriority); prop != nil {
		if v, err := strconv.ParseInt(prop.Value, 10, 64); err == nil {
			priority = v
		}
	}

	var url string
	if prop := ve.Props.Get(ical.PropURL); prop != nil {
		url = prop.Value
	}

	categories := parseCategories(ve)
	exdates := parseDateList(ve, ical.PropExceptionDates)
	rdates := parseDateList(ve, ical.PropRecurrenceDates)

	var recurrenceID string
	if prop := ve.Props.Get(ical.PropRecurrenceID); prop != nil {
		if rid, err := ve.Props.DateTime(ical.PropRecurrenceID, nil); err == nil && !rid.IsZero() {
			recurrenceID = rid.UTC().Format(time.RFC3339)
		}
	}

	// VALARM children
	var alarms []model.Alarm
	for _, child := range ve.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		alarm := parseAlarm(child)
		if alarm.TriggerValue != "" {
			alarms = append(alarms, alarm)
		}
	}

	// ATTENDEE + ORGANIZER
	attendees := parseAttendees(ve)

	return event.Event{
		UID:            uid,
		Title:          summary,
		Description:    description,
		Location:       location,
		StartTime:      startTime.UTC(),
		EndTime:        endTime.UTC(),
		AllDay:         allDay,
		RecurrenceRule: rrule,
		Timezone:       timezone,
		Status:         strings.ToUpper(status),
		Transp:         strings.ToUpper(transp),
		Sequence:       sequence,
		Priority:       priority,
		Class:          strings.ToUpper(class),
		URL:            url,
		Categories:     categories,
		ExDates:        exdates,
		RDates:         rdates,
		RecurrenceID:   recurrenceID,
		Alarms:         alarms,
		Attendees:      attendees,
	}, nil
}

func textOrDefault(ve ical.Event, prop, def string) string {
	if v, err := ve.Props.Text(prop); err == nil && v != "" {
		return v
	}
	return def
}

func parseCategories(ve ical.Event) string {
	var cats []string
	for _, prop := range ve.Props.Values(ical.PropCategories) {
		// Use Text() to decode iCal escaping (\, → ,), then split
		decoded, err := prop.Text()
		if err != nil {
			decoded = prop.Value
		}
		parts := strings.Split(decoded, ",")
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				cats = append(cats, s)
			}
		}
	}
	return strings.Join(cats, ",")
}

func parseDateList(ve ical.Event, propName string) string {
	var dates []string
	for _, prop := range ve.Props.Values(propName) {
		// Each property may contain comma-separated dates
		parts := strings.Split(prop.Value, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// Try parsing as datetime
			for _, layout := range []string{
				"20060102T150405Z",
				"20060102T150405",
				"20060102",
				time.RFC3339,
			} {
				if t, err := time.Parse(layout, p); err == nil {
					dates = append(dates, t.UTC().Format(time.RFC3339))
					break
				}
			}
		}
	}
	return strings.Join(dates, ",")
}

func parseAlarm(comp *ical.Component) model.Alarm {
	alarm := model.Alarm{Action: "DISPLAY"}

	if prop := comp.Props.Get(ical.PropAction); prop != nil {
		alarm.Action = strings.ToUpper(prop.Value)
	}
	if prop := comp.Props.Get(ical.PropTrigger); prop != nil {
		alarm.TriggerValue = prop.Value
	}
	if prop := comp.Props.Get(ical.PropDescription); prop != nil {
		alarm.Description = prop.Value
	}

	return alarm
}

func parseAttendees(ve ical.Event) []model.Attendee {
	var attendees []model.Attendee

	// ORGANIZER
	if prop := ve.Props.Get(ical.PropOrganizer); prop != nil {
		attendees = append(attendees, model.Attendee{
			Email:      stripMailto(prop.Value),
			Name:       prop.Params.Get(ical.ParamCommonName),
			RSVPStatus: "ACCEPTED",
			Role:       "CHAIR",
			Organizer:  true,
		})
	}

	// ATTENDEE properties
	for _, prop := range ve.Props.Values(ical.PropAttendee) {
		a := model.Attendee{
			Email:      stripMailto(prop.Value),
			Name:       prop.Params.Get(ical.ParamCommonName),
			RSVPStatus: strings.ToUpper(paramOrDefault(&prop, ical.ParamParticipationStatus, "NEEDS-ACTION")),
			Role:       strings.ToUpper(paramOrDefault(&prop, ical.ParamRole, "REQ-PARTICIPANT")),
		}
		attendees = append(attendees, a)
	}

	return attendees
}

func stripMailto(s string) string {
	return strings.TrimPrefix(strings.TrimPrefix(s, "mailto:"), "MAILTO:")
}

func paramOrDefault(prop *ical.Prop, param, def string) string {
	if v := prop.Params.Get(param); v != "" {
		return v
	}
	return def
}
