package ical

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// todoSpanStorable reports whether the DTSTART plus the DURATION lands on
// a time the database can hold. See timeutil.Storable for that rule. An
// unparseable start gives no end to test, so the function accepts it. The
// todo service applies the same rule in validateTiming.
func todoSpanStorable(startDate, dur string) bool {
	start := timeutil.ParseDate(startDate)
	if start.IsZero() {
		return true
	}
	return timeutil.Storable(duration.Add(start, dur))
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

	var todoWarnings []string
	dueDate, w := parseDateProp(props, "todo", ical.PropDue, uid)
	if w != "" {
		todoWarnings = append(todoWarnings, w)
	}
	startDate, w := parseDateProp(props, "todo", ical.PropDateTimeStart, uid)
	if w != "" {
		todoWarnings = append(todoWarnings, w)
	}

	// Screen the DURATION on the same four rules the todo service
	// applies. The value must be a positive span (RFC 5545 §3.8.2.5).
	// DUE and DURATION are mutually exclusive, and a DURATION needs a
	// DTSTART (§3.6.2). The end must also land on a time the database
	// can hold.
	//
	// Drop the DURATION and warn when a rule fails. A stored bad value
	// would re-export verbatim and would poison a RELATED=END alarm
	// anchor. A rejected shape would fail the whole calendar pull on
	// every run, because the sync engine stops at the first resource
	// error.
	var durationValue string
	if prop := props.Get(ical.PropDuration); prop != nil {
		switch {
		case duration.ValidateSpan(prop.Value) != nil:
			todoWarnings = append(todoWarnings, fmt.Sprintf(
				"todo %q: unusable DURATION %q; dropped", uid, prop.Value))
		case dueDate != "":
			todoWarnings = append(todoWarnings, fmt.Sprintf(
				"todo %q: DUE and DURATION are mutually exclusive; DURATION %q dropped", uid, prop.Value))
		case startDate == "":
			todoWarnings = append(todoWarnings, fmt.Sprintf(
				"todo %q: DURATION %q needs a DTSTART; dropped", uid, prop.Value))
		case !todoSpanStorable(startDate, prop.Value):
			todoWarnings = append(todoWarnings, fmt.Sprintf(
				"todo %q: DURATION %q ends past year %d; dropped", uid, prop.Value, timeutil.MaxStorableYear))
		default:
			durationValue = prop.Value
		}
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
	exdates := parseDateListFromProps(props, ical.PropExceptionDates, dtstartZone(props))
	rdates := parseDateListFromProps(props, ical.PropRecurrenceDates, dtstartZone(props))
	var rrule string
	if prop := props.Get(ical.PropRecurrenceRule); prop != nil {
		rrule = prop.Value
	}

	var geo string
	if prop := props.Get(ical.PropGeo); prop != nil {
		geo = prop.Value
	}

	recurrenceID, err := parseRecurrenceID(props)
	if err != nil {
		return todo.Todo{}, nil, err
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
			// Name the owning record: the dropped alarm leaves no other trace.
			alarmWarnings = append(alarmWarnings, fmt.Sprintf("todo %q: %s", uid, w))
		}
		if alarm.TriggerValue != "" {
			alarms = append(alarms, alarm)
		}
	}

	// ATTENDEE + ORGANIZER
	attendees, attendeeWarnings := parseAttendeesFromProps(props, model.TaskAttendee)
	for _, w := range attendeeWarnings {
		// Name the owning record: the clamp leaves no other trace.
		todoWarnings = append(todoWarnings, fmt.Sprintf("todo %q: %s", uid, w))
	}

	// ATTACH, COMMENT, CONTACT, RELATED-TO
	attachments, err := parseAttachmentsFromProps(props)
	if err != nil {
		return todo.Todo{}, nil, err
	}
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
		Duration:        durationValue,
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
		XProperties:     extractXPropertiesWithSet(props, handledTodoProps),
	}, append(alarmWarnings, todoWarnings...), nil
}

// handledTodoProps is the set of property names explicitly parsed by todoFromVTodo.
var handledTodoProps = map[string]bool{
	ical.PropUID: true, ical.PropSummary: true, ical.PropDescription: true,
	ical.PropLocation: true, ical.PropDateTimeStart: true, ical.PropDue: true,
	ical.PropDuration: true, ical.PropCompleted: true, ical.PropPercentComplete: true,
	ical.PropRecurrenceRule: true, ical.PropStatus: true,
	"SEQUENCE": true, ical.PropPriority: true,
	ical.PropClass: true, ical.PropURL: true, ical.PropGeo: true,
	ical.PropCategories: true, ical.PropExceptionDates: true,
	ical.PropRecurrenceDates: true, ical.PropRecurrenceID: true,
	ical.PropDateTimeStamp: true, ical.PropCreated: true, ical.PropLastModified: true,
	ical.PropAttach: true, ical.PropComment: true, ical.PropContact: true,
	ical.PropResources: true, ical.PropRelatedTo: true,
	ical.PropAttendee: true, ical.PropOrganizer: true,
}
