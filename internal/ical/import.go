package ical

import (
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/tcal/internal/duration"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/todo"
)

type ImportResult struct {
	Events []event.Event
	Todos  []todo.Todo
}

func ImportFile(r io.Reader) (ImportResult, error) {
	dec := ical.NewDecoder(r)
	var result ImportResult

	for {
		cal, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, fmt.Errorf("decode ical: %w", err)
		}

		for _, child := range cal.Children {
			switch child.Name {
			case ical.CompEvent:
				vevent := ical.Event{Component: child}
				e, err := eventFromVEvent(vevent)
				if err == nil {
					result.Events = append(result.Events, e)
				}
			case ical.CompToDo:
				t, err := todoFromVTodo(child)
				if err == nil {
					result.Todos = append(result.Todos, t)
				}
			}
		}
	}

	return result, nil
}

func todoFromVTodo(comp *ical.Component) (todo.Todo, error) {
	props := comp.Props

	uid := propText(props, ical.PropUID)
	if uid == "" {
		return todo.Todo{}, fmt.Errorf("missing UID")
	}

	summary := propText(props, ical.PropSummary)
	description := propText(props, ical.PropDescription)
	location := propText(props, ical.PropLocation)

	var dueDate string
	if prop := props.Get(ical.PropDue); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			dueDate = t.UTC().Format(time.RFC3339)
		}
	}

	var startDate string
	if prop := props.Get(ical.PropDateTimeStart); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			startDate = t.UTC().Format(time.RFC3339)
		}
	}

	var duration string
	if prop := props.Get(ical.PropDuration); prop != nil {
		duration = prop.Value
	}

	var completedAt string
	if prop := props.Get(ical.PropCompleted); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			completedAt = t.UTC().Format(time.RFC3339)
		}
	}

	var percentComplete int64
	if prop := props.Get(ical.PropPercentComplete); prop != nil {
		if v, err := strconv.ParseInt(prop.Value, 10, 64); err == nil {
			percentComplete = v
		}
	}

	status := propTextOr(props, ical.PropStatus, "NEEDS-ACTION")
	class := propTextOr(props, ical.PropClass, "PUBLIC")

	var priority int64
	if prop := props.Get(ical.PropPriority); prop != nil {
		if v, err := strconv.ParseInt(prop.Value, 10, 64); err == nil {
			priority = v
		}
	}

	var sequence int64
	if prop := props.Get("SEQUENCE"); prop != nil {
		if v, err := strconv.ParseInt(prop.Value, 10, 64); err == nil {
			sequence = v
		}
	}

	url := propText(props, ical.PropURL)

	var timezone string
	if prop := props.Get(ical.PropDateTimeStart); prop != nil {
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			timezone = tzid
		}
	}

	categories := parseCategoriesFromProps(props)
	exdates := parseDateListFromProps(props, ical.PropExceptionDates)
	rdates := parseDateListFromProps(props, ical.PropRecurrenceDates)
	var rrule string
	if prop := props.Get(ical.PropRecurrenceRule); prop != nil {
		rrule = prop.Value
	}

	var geo string
	if prop := props.Get(ical.PropGeo); prop != nil {
		geo = prop.Value
	}

	var recurrenceID string
	if prop := props.Get(ical.PropRecurrenceID); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			recurrenceID = t.UTC().Format(time.RFC3339)
		}
	}

	// VALARM children
	var alarms []model.Alarm
	for _, child := range comp.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		alarm := parseAlarm(child)
		if alarm.TriggerValue != "" {
			alarms = append(alarms, alarm)
		}
	}

	// ATTENDEE + ORGANIZER
	attendees := parseAttendeesFromProps(props)

	// ATTACH, COMMENT, CONTACT, RELATED-TO
	attachments := parseAttachmentsFromProps(props)
	comments := parseCommentsFromProps(props)
	contacts := parseContactsFromProps(props)
	resources := parseResourcesFromProps(props)
	relations := parseRelationsFromProps(props)

	return todo.Todo{
		UID:             uid,
		Summary:         summary,
		Description:     description,
		Location:        location,
		DueDate:         dueDate,
		StartDate:       startDate,
		Duration:        duration,
		CompletedAt:     completedAt,
		PercentComplete: percentComplete,
		Status:          strings.ToUpper(status),
		Priority:        priority,
		Class:           strings.ToUpper(class),
		URL:             url,
		Categories:      categories,
		RecurrenceRule:  rrule,
		Timezone:        timezone,
		Sequence:        sequence,
		ExDates:         exdates,
		RDates:          rdates,
		RecurrenceID:    recurrenceID,
		Geo:             geo,
		Alarms:          alarms,
		Attendees:       attendees,
		Attachments:     attachments,
		Comments:        comments,
		Contacts:        contacts,
		Resources:       resources,
		Relations:       relations,
	}, nil
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

	var endTime time.Time
	if prop := ve.Props.Get(ical.PropDateTimeEnd); prop != nil {
		endTime, _ = ve.Props.DateTime(ical.PropDateTimeEnd, nil)
	}
	if endTime.IsZero() {
		if prop := ve.Props.Get(ical.PropDuration); prop != nil {
			endTime = addDuration(startTime, prop.Value)
		} else {
			endTime = startTime.Add(time.Hour)
		}
	}

	allDay := false
	if prop := ve.Props.Get(ical.PropDateTimeStart); prop != nil {
		if strings.EqualFold(prop.Params.Get("VALUE"), "DATE") {
			allDay = true
		}
	}

	var rrule string
	if prop := ve.Props.Get(ical.PropRecurrenceRule); prop != nil {
		rrule = prop.Value
	}

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

	var geo string
	if prop := ve.Props.Get(ical.PropGeo); prop != nil {
		geo = prop.Value
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

	// ATTACH, COMMENT, RELATED-TO
	attachments := parseAttachmentsFromProps(ve.Props)
	comments := parseCommentsFromProps(ve.Props)
	contacts := parseContactsFromProps(ve.Props)
	resources := parseResourcesFromProps(ve.Props)
	relations := parseRelationsFromProps(ve.Props)

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
		Geo:            geo,
		Alarms:         alarms,
		Attendees:      attendees,
		Attachments:    attachments,
		Comments:       comments,
		Contacts:       contacts,
		Resources:      resources,
		Relations:      relations,
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
	alarm := model.Alarm{Action: "DISPLAY", Related: "START"}

	if prop := comp.Props.Get(ical.PropAction); prop != nil {
		alarm.Action = strings.ToUpper(prop.Value)
	}
	if prop := comp.Props.Get(ical.PropTrigger); prop != nil {
		alarm.TriggerValue = prop.Value
		if rel := prop.Params.Get("RELATED"); rel != "" {
			alarm.Related = strings.ToUpper(rel)
		}
	}
	if prop := comp.Props.Get(ical.PropDescription); prop != nil {
		alarm.Description = prop.Value
	}
	if prop := comp.Props.Get("REPEAT"); prop != nil {
		if v, err := strconv.Atoi(prop.Value); err == nil {
			alarm.Repeat = v
		}
	}
	if prop := comp.Props.Get(ical.PropDuration); prop != nil {
		alarm.Duration = prop.Value
	}

	// ATTENDEE children (for EMAIL alarms)
	for _, prop := range comp.Props.Values(ical.PropAttendee) {
		alarm.Attendees = append(alarm.Attendees, model.AlarmAttendee{
			Email: stripMailto(prop.Value),
			Name:  prop.Params.Get(ical.ParamCommonName),
		})
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

// Props-based helpers for VTODO (no wrapper type in go-ical)

func propText(props ical.Props, name string) string {
	if prop := props.Get(name); prop != nil {
		if v, err := prop.Text(); err == nil {
			return v
		}
		return prop.Value
	}
	return ""
}

func propTextOr(props ical.Props, name, def string) string {
	if v := propText(props, name); v != "" {
		return v
	}
	return def
}

func parseCategoriesFromProps(props ical.Props) string {
	var cats []string
	for _, prop := range props.Values(ical.PropCategories) {
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

func parseDateListFromProps(props ical.Props, propName string) string {
	var dates []string
	for _, prop := range props.Values(propName) {
		parts := strings.Split(prop.Value, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
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

func parseAttendeesFromProps(props ical.Props) []model.Attendee {
	var attendees []model.Attendee

	if prop := props.Get(ical.PropOrganizer); prop != nil {
		attendees = append(attendees, model.Attendee{
			Email:      stripMailto(prop.Value),
			Name:       prop.Params.Get(ical.ParamCommonName),
			RSVPStatus: "ACCEPTED",
			Role:       "CHAIR",
			Organizer:  true,
		})
	}

	for _, prop := range props.Values(ical.PropAttendee) {
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

// addDuration parses an RFC 5545 duration string and adds it to a time.
// Format: [+/-]P[nW] or [+/-]P[nD][T[nH][nM][nS]]
func addDuration(t time.Time, dur string) time.Time {
	return duration.Add(t, dur)
}

func parseAttachmentsFromProps(props ical.Props) []model.Attachment {
	var out []model.Attachment
	for _, prop := range props.Values(ical.PropAttach) {
		fmttype := prop.Params.Get("FMTTYPE")
		if prop.Params.Get("ENCODING") == "BASE64" {
			data, err := base64.StdEncoding.DecodeString(prop.Value)
			if err != nil {
				continue
			}
			out = append(out, model.Attachment{
				FmtType:  fmttype,
				Data:     data,
				Filename: prop.Params.Get("FILENAME"),
			})
		} else {
			out = append(out, model.Attachment{
				URI:     prop.Value,
				FmtType: fmttype,
			})
		}
	}
	return out
}

func parseCommentsFromProps(props ical.Props) []string {
	var out []string
	for _, prop := range props.Values(ical.PropComment) {
		text, err := prop.Text()
		if err != nil {
			text = prop.Value
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func parseContactsFromProps(props ical.Props) []string {
	var out []string
	for _, prop := range props.Values(ical.PropContact) {
		text, err := prop.Text()
		if err != nil {
			text = prop.Value
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func parseResourcesFromProps(props ical.Props) []string {
	var out []string
	for _, prop := range props.Values(ical.PropResources) {
		// RESOURCES can be comma-separated within a single property
		text, err := prop.Text()
		if err != nil {
			text = prop.Value
		}
		for _, r := range strings.Split(text, ",") {
			if s := strings.TrimSpace(r); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func parseRelationsFromProps(props ical.Props) []model.Relation {
	var out []model.Relation
	for _, prop := range props.Values(ical.PropRelatedTo) {
		relType := prop.Params.Get("RELTYPE")
		if relType == "" {
			relType = "PARENT" // default per RFC 5545
		}
		if prop.Value != "" {
			out = append(out, model.Relation{
				RelType: strings.ToUpper(relType),
				RelUID:  prop.Value,
			})
		}
	}
	return out
}
