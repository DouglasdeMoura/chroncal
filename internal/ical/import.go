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
	Events   []event.Event
	Todos    []todo.Todo
	Warnings []string
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

		// Build timezone map from VTIMEZONE components.
		tzMap := buildTZMap(cal)

		skipped := make(map[string]int)
		for _, child := range cal.Children {
			switch child.Name {
			case ical.CompEvent:
				vevent := ical.Event{Component: child}
				resolveComponentTZIDs(child, tzMap)
				e, warns, err := eventFromVEvent(vevent)
				if err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("VEVENT: %v", err))
					continue
				}
				result.Warnings = append(result.Warnings, warns...)
				result.Events = append(result.Events, e)
			case ical.CompToDo:
				resolveComponentTZIDs(child, tzMap)
				t, warns, err := todoFromVTodo(child)
				if err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("VTODO: %v", err))
					continue
				}
				result.Warnings = append(result.Warnings, warns...)
				result.Todos = append(result.Todos, t)
			default:
				if child.Name != "VTIMEZONE" {
					skipped[child.Name]++
				}
			}
		}
		for name, count := range skipped {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped: %s (%d)", name, count))
		}
	}

	return result, nil
}

func todoFromVTodo(comp *ical.Component) (todo.Todo, []string, error) {
	props := comp.Props

	uid := propText(props, ical.PropUID)
	if uid == "" {
		return todo.Todo{}, nil, fmt.Errorf("missing UID")
	}

	summary := propText(props, ical.PropSummary)
	description := propText(props, ical.PropDescription)
	location := propText(props, ical.PropLocation)

	var dueDate string
	if prop := props.Get(ical.PropDue); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			if len(prop.Value) == 8 {
				dueDate = t.Format("2006-01-02")
			} else {
				dueDate = t.UTC().Format(time.RFC3339)
			}
		}
	}

	var startDate string
	if prop := props.Get(ical.PropDateTimeStart); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			if len(prop.Value) == 8 {
				startDate = t.Format("2006-01-02")
			} else {
				startDate = t.UTC().Format(time.RFC3339)
			}
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
	var todoFloating bool
	if prop := props.Get(ical.PropDateTimeStart); prop != nil {
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			timezone = tzid
		} else if len(prop.Value) > 8 && !strings.HasSuffix(prop.Value, "Z") {
			todoFloating = true
		}
	}
	if timezone == "" && !todoFloating {
		if prop := props.Get(ical.PropDue); prop != nil {
			if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
				timezone = tzid
			} else if len(prop.Value) > 8 && !strings.HasSuffix(prop.Value, "Z") {
				todoFloating = true
			}
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

	var dtstamp string
	if prop := props.Get(ical.PropDateTimeStamp); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			dtstamp = t.UTC().Format(time.RFC3339)
		}
	}

	// VALARM children
	var alarms []model.Alarm
	var alarmWarnings []string
	for _, child := range comp.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		alarm, w := parseAlarm(child)
		if w != "" {
			alarmWarnings = append(alarmWarnings, w)
		}
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
		Timezone:        floatingOrTZ(todoFloating, timezone),
		Sequence:        sequence,
		ExDates:         exdates,
		RDates:          rdates,
		RecurrenceID:    recurrenceID,
		Geo:             geo,
		DtStamp:         dtstamp,
		Alarms:          alarms,
		Attendees:       attendees,
		Attachments:     attachments,
		Comments:        comments,
		Contacts:        contacts,
		Resources:       resources,
		Relations:       relations,
	}, alarmWarnings, nil
}

func eventFromVEvent(ve ical.Event) (event.Event, []string, error) {
	uid, err := ve.Props.Text(ical.PropUID)
	if err != nil || uid == "" {
		return event.Event{}, nil, fmt.Errorf("missing UID")
	}

	summary, _ := ve.Props.Text(ical.PropSummary)
	description, _ := ve.Props.Text(ical.PropDescription)
	location, _ := ve.Props.Text(ical.PropLocation)

	// Timezone from DTSTART param
	var timezone string
	var floating bool
	if prop := ve.Props.Get(ical.PropDateTimeStart); prop != nil {
		tzid := prop.Params.Get(ical.ParamTimezoneID)
		if tzid != "" {
			timezone = tzid
		} else if !strings.EqualFold(prop.Params.Get("VALUE"), "DATE") &&
			!strings.HasSuffix(prop.Value, "Z") {
			// No TZID, not all-day, no Z suffix → floating time.
			floating = true
		}
	}

	startTime, err := ve.Props.DateTime(ical.PropDateTimeStart, nil)
	if err != nil {
		return event.Event{}, nil, fmt.Errorf("parse DTSTART: %w", err)
	}

	var endTime time.Time
	var durationValue string
	if prop := ve.Props.Get(ical.PropDateTimeEnd); prop != nil {
		endTime, _ = ve.Props.DateTime(ical.PropDateTimeEnd, nil)
	}
	if endTime.IsZero() {
		if prop := ve.Props.Get(ical.PropDuration); prop != nil {
			durationValue = prop.Value
			endTime = addDuration(startTime, prop.Value)
		} else {
			endTime = startTime.Add(time.Hour)
		}
	}

	allDay := false
	if prop := ve.Props.Get(ical.PropDateTimeStart); prop != nil {
		if strings.EqualFold(prop.Params.Get("VALUE"), "DATE") {
			allDay = true
			// VALUE=DATE represents a calendar date, not a specific UTC instant.
			// Store as midnight in the local timezone so the date component is
			// preserved regardless of the machine's UTC offset. This keeps
			// round-trips stable: export → import produces the same local date.
			startTime = time.Date(startTime.Year(), startTime.Month(), startTime.Day(), 0, 0, 0, 0, time.Local)
			endTime = time.Date(endTime.Year(), endTime.Month(), endTime.Day(), 0, 0, 0, 0, time.Local)
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
	exdates := parseDateListFromProps(ve.Props, ical.PropExceptionDates)
	rdates := parseDateListFromProps(ve.Props, ical.PropRecurrenceDates)

	var recurrenceID string
	if prop := ve.Props.Get(ical.PropRecurrenceID); prop != nil {
		if rid, err := ve.Props.DateTime(ical.PropRecurrenceID, nil); err == nil && !rid.IsZero() {
			recurrenceID = rid.UTC().Format(time.RFC3339)
		}
	}

	var dtstamp string
	if prop := ve.Props.Get(ical.PropDateTimeStamp); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			dtstamp = t.UTC().Format(time.RFC3339)
		}
	}

	// VALARM children
	var alarms []model.Alarm
	var alarmWarnings []string
	for _, child := range ve.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		alarm, w := parseAlarm(child)
		if w != "" {
			alarmWarnings = append(alarmWarnings, w)
		}
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
		Timezone:       floatingOrTZ(floating, timezone),
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
		DurationValue:  durationValue,
		DtStamp:        dtstamp,
		Alarms:         alarms,
		Attendees:      attendees,
		Attachments:    attachments,
		Comments:       comments,
		Contacts:       contacts,
		Resources:      resources,
		Relations:      relations,
	}, alarmWarnings, nil
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
		// TextList splits on unescaped commas and unescapes each value,
		// handling both RFC-correct "CATEGORIES:a,b" and legacy
		// escaped "CATEGORIES:a\,b" inputs.
		if list, err := prop.TextList(); err == nil {
			for _, s := range list {
				if s = strings.TrimSpace(s); s != "" {
					cats = append(cats, s)
				}
			}
		}
	}
	return strings.Join(cats, ",")
}

// parseAlarm extracts a model.Alarm from a VALARM component.
// The second return value is a warning string (empty if no issues).
func parseAlarm(comp *ical.Component) (model.Alarm, string) {
	alarm := model.Alarm{Action: "DISPLAY", Related: "START"}
	var warn string

	if prop := comp.Props.Get(ical.PropAction); prop != nil {
		alarm.Action = strings.ToUpper(prop.Value)
	}
	if prop := comp.Props.Get(ical.PropTrigger); prop != nil {
		tv := prop.Value
		tzid := prop.Params.Get(ical.ParamTimezoneID)
		// Validate trigger: must be a parseable duration or datetime.
		valid := false
		if duration.Validate(tv) == nil {
			valid = true
		} else if _, err := time.Parse("20060102T150405Z", tv); err == nil {
			valid = true
		} else if tzid != "" {
			// TRIGGER;TZID=X:YYYYMMDDTHHMMSS — resolve to UTC.
			if t, err := time.Parse("20060102T150405", tv); err == nil {
				if loc, err := time.LoadLocation(tzid); err == nil {
					t = time.Date(t.Year(), t.Month(), t.Day(),
						t.Hour(), t.Minute(), t.Second(), 0, loc)
					tv = t.UTC().Format("20060102T150405Z")
				} else {
					warn = fmt.Sprintf("VALARM TRIGGER TZID=%s: unknown timezone, treating as floating", tzid)
				}
				// Valid as floating even if TZID resolution failed.
				valid = true
			}
		} else if _, err := time.Parse("20060102T150405", tv); err == nil {
			valid = true
		} else if _, err := time.Parse(time.RFC3339, tv); err == nil {
			valid = true
		}
		if valid {
			alarm.TriggerValue = tv
		}
		if rel := prop.Params.Get("RELATED"); rel != "" {
			alarm.Related = strings.ToUpper(rel)
		}
	}
	if prop := comp.Props.Get(ical.PropDescription); prop != nil {
		alarm.Description = prop.Value
	}
	if prop := comp.Props.Get(ical.PropSummary); prop != nil {
		alarm.Summary = prop.Value
	}
	if prop := comp.Props.Get("REPEAT"); prop != nil {
		if v, err := strconv.Atoi(prop.Value); err == nil {
			alarm.Repeat = v
		}
	}
	if prop := comp.Props.Get(ical.PropDuration); prop != nil {
		alarm.Duration = prop.Value
	}

	// UID (RFC 9074)
	if prop := comp.Props.Get(ical.PropUID); prop != nil {
		uid := strings.TrimSpace(prop.Value)
		if len(uid) > 0 && len(uid) <= 255 && !strings.ContainsRune(uid, 0) {
			alarm.UID = uid
		}
	}

	// ACKNOWLEDGED (RFC 9074) — preserved for round-trip fidelity only.
	if prop := comp.Props.Get("ACKNOWLEDGED"); prop != nil {
		v := strings.TrimSpace(prop.Value)
		if model.ValidateAcknowledged(v) && v != "" {
			alarm.Acknowledged = v
		}
	}

	// ATTACH (sound URI for AUDIO alarms)
	if prop := comp.Props.Get(ical.PropAttach); prop != nil {
		if prop.Params.Get("ENCODING") != "BASE64" {
			alarm.AttachURI = prop.Value
			alarm.AttachFmtType = prop.Params.Get("FMTTYPE")
		}
	}

	// ATTENDEE children (for EMAIL alarms)
	for _, prop := range comp.Props.Values(ical.PropAttendee) {
		alarm.Attendees = append(alarm.Attendees, model.AlarmAttendee{
			Email: stripMailto(prop.Value),
			Name:  prop.Params.Get(ical.ParamCommonName),
		})
	}

	return alarm, warn
}

func parseAttendees(ve ical.Event) []model.Attendee {
	var attendees []model.Attendee

	// ORGANIZER — track email so we can deduplicate against ATTENDEE below.
	var organizerEmail string
	if prop := ve.Props.Get(ical.PropOrganizer); prop != nil {
		organizerEmail = stripMailto(prop.Value)
		attendees = append(attendees, model.Attendee{
			Email:      organizerEmail,
			Name:       prop.Params.Get(ical.ParamCommonName),
			RSVPStatus: "ACCEPTED",
			Role:       "CHAIR",
			Organizer:  true,
			SentBy:     stripMailto(prop.Params.Get(ical.ParamSentBy)),
			Dir:        prop.Params.Get(ical.ParamDir),
			Language:   prop.Params.Get(ical.ParamLanguage),
		})
	}

	// ATTENDEE properties — skip duplicates of the ORGANIZER email.
	for _, prop := range ve.Props.Values(ical.PropAttendee) {
		email := stripMailto(prop.Value)
		if organizerEmail != "" && strings.EqualFold(email, organizerEmail) {
			continue
		}
		a := attendeeFromProp(&prop)
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
		if list, err := prop.TextList(); err == nil {
			for _, s := range list {
				if s = strings.TrimSpace(s); s != "" {
					cats = append(cats, s)
				}
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
					// Preserve date-only format for VALUE=DATE
					if layout == "20060102" {
						dates = append(dates, t.Format("2006-01-02"))
					} else {
						dates = append(dates, t.UTC().Format(time.RFC3339))
					}
					break
				}
			}
		}
	}
	return strings.Join(dates, ",")
}

func parseAttendeesFromProps(props ical.Props) []model.Attendee {
	var attendees []model.Attendee

	var organizerEmail string
	if prop := props.Get(ical.PropOrganizer); prop != nil {
		organizerEmail = stripMailto(prop.Value)
		attendees = append(attendees, model.Attendee{
			Email:      organizerEmail,
			Name:       prop.Params.Get(ical.ParamCommonName),
			RSVPStatus: "ACCEPTED",
			Role:       "CHAIR",
			Organizer:  true,
			SentBy:     stripMailto(prop.Params.Get(ical.ParamSentBy)),
			Dir:        prop.Params.Get(ical.ParamDir),
			Language:   prop.Params.Get(ical.ParamLanguage),
		})
	}

	for _, prop := range props.Values(ical.PropAttendee) {
		email := stripMailto(prop.Value)
		if organizerEmail != "" && strings.EqualFold(email, organizerEmail) {
			continue
		}
		a := attendeeFromProp(&prop)
		attendees = append(attendees, a)
	}

	return attendees
}

// attendeeFromProp extracts a model.Attendee from an iCal ATTENDEE property,
// including all RFC 5545 parameters.
func attendeeFromProp(prop *ical.Prop) model.Attendee {
	return model.Attendee{
		Email:         stripMailto(prop.Value),
		Name:          prop.Params.Get(ical.ParamCommonName),
		RSVPStatus:    strings.ToUpper(paramOrDefault(prop, ical.ParamParticipationStatus, "NEEDS-ACTION")),
		Role:          strings.ToUpper(paramOrDefault(prop, ical.ParamRole, "REQ-PARTICIPANT")),
		CUType:        strings.ToUpper(paramOrDefault(prop, ical.ParamCalendarUserType, "INDIVIDUAL")),
		RSVPRequested: strings.EqualFold(prop.Params.Get(ical.ParamRSVP), "TRUE"),
		SentBy:        stripMailto(prop.Params.Get(ical.ParamSentBy)),
		DelegatedTo:   joinMailtoParams(prop.Params.Values(ical.ParamDelegatedTo)),
		DelegatedFrom: joinMailtoParams(prop.Params.Values(ical.ParamDelegatedFrom)),
		Member:        joinMailtoParams(prop.Params.Values(ical.ParamMember)),
		Dir:           prop.Params.Get(ical.ParamDir),
		Language:      prop.Params.Get(ical.ParamLanguage),
	}
}

// joinMailtoParams joins multiple mailto URI param values into a comma-separated
// string, stripping the "mailto:" prefix and surrounding quotes from each.
func joinMailtoParams(values []string) string {
	if len(values) == 0 {
		return ""
	}
	cleaned := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.Trim(v, "\"")
		v = stripMailto(v)
		if v != "" {
			cleaned = append(cleaned, v)
		}
	}
	return strings.Join(cleaned, ",")
}

// floatingOrTZ returns "FLOATING" if the time was detected as floating,
// otherwise returns the original timezone string.
func floatingOrTZ(floating bool, tz string) string {
	if floating {
		return "FLOATING"
	}
	return tz
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
		// RESOURCES is a comma-separated list (like CATEGORIES).
		// Use TextList to split on unescaped commas correctly.
		if list, err := prop.TextList(); err == nil {
			for _, s := range list {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
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
