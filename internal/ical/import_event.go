package ical

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

func eventFromVEvent(ve ical.Event, remote bool) (event.Event, []string, error) {
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
	var dtendWarnings []string
	explicitEnd := false
	badDTEND := false
	var badDTENDRaw *ical.Prop
	if prop := ve.Props.Get(ical.PropDateTimeEnd); prop != nil {
		var err error
		endTime, err = prop.DateTime(nil)
		if err != nil {
			// Malformed DTEND (go-ical stores the raw value without
			// validating). Props.Get above guarantees the property exists, so
			// the only error DateTime can return here is a parse failure.
			//
			// Discard it and fall through as if DTEND were absent: a valid
			// DURATION on the same component is still the better answer, and
			// leaving explicitEnd false keeps the all-day branch below free to
			// apply the RFC 5545 implicit one-day span. Forcing an explicit end
			// here instead would collapse an all-day event to zero duration —
			// the exact outcome this fallback exists to prevent.
			endTime = time.Time{}
			badDTEND = true
			badDTENDRaw = prop
			dtendWarnings = append(dtendWarnings, fmt.Sprintf(
				"event %q: unparseable DTEND %q; ignored, falling back to DURATION or the default span", uid, prop.Value))
		}
	}
	if endTime.IsZero() {
		prop := ve.Props.Get(ical.PropDuration)
		spanOK := false
		if prop != nil && duration.ValidateSpan(prop.Value) == nil {
			// The end must land on a time the database can hold. See
			// timeutil.Storable for that rule.
			if end := addDuration(startTime, prop.Value); timeutil.Storable(end) {
				durationValue = prop.Value
				endTime = end
				explicitEnd = true
				spanOK = true
			}
		}
		if spanOK {
			// The DURATION above is the span.
		} else if prop != nil && duration.Validate(prop.Value) == nil &&
			addDuration(startTime, prop.Value).Equal(startTime) {
			// An exact zero span (DURATION:PT0S). RFC 5545 §3.6.1 gives
			// the same meaning to a VEVENT with no DTEND and no
			// DURATION, so store the equivalent zero-length event. Drop
			// the value; DTEND=DTSTART carries the semantics on export.
			endTime = startTime
			explicitEnd = true
		} else if prop != nil {
			// Malformed, negative, or unstorable DURATION (go-ical
			// stores the raw value with no validation). Fall back to a
			// 1h span. Warn the user. Drop the bad value, so it does
			// not persist and does not re-export.
			endTime = startTime.Add(time.Hour)
			dtendWarnings = append(dtendWarnings, fmt.Sprintf(
				"event %q: unusable DURATION %q; dropped, the event gets a 1h span", uid, prop.Value))
		} else if badDTEND {
			// DTEND was present but unusable and there is no DURATION to fall
			// back on. A timed event gets the same 1h default as a malformed
			// DURATION rather than collapsing to zero duration; an all-day one
			// falls through to the implicit one-day span below, since
			// explicitEnd is still false.
			endTime = startTime.Add(time.Hour)
		} else {
			// RFC 5545 §3.6.1: a VEVENT with DTSTART as DATE-TIME and no
			// DTEND/DURATION is instantaneous (zero duration). The all-day
			// case (VALUE=DATE) is handled below to apply the implicit one-day
			// span instead.
			endTime = startTime
		}
	} else {
		explicitEnd = true
	}

	allDay := false
	if prop := ve.Props.Get(ical.PropDateTimeStart); prop != nil {
		if strings.EqualFold(prop.Params.Get("VALUE"), "DATE") {
			allDay = true
			// VALUE=DATE represents a calendar date, not a specific UTC instant.
			// Store as midnight UTC so the stored instant is independent of the
			// importing host's timezone (AGENTS.md: "All database times are
			// RFC 3339 strings in UTC"; "All-day events have time component
			// 00:00:00"). Using time.Local here would store a host-dependent
			// instant — e.g. under TZ=UTC+12 midnight local is 12:00Z the day
			// before, corrupting the calendar date and recurrence occurrences.
			startTime = time.Date(startTime.Year(), startTime.Month(), startTime.Day(), 0, 0, 0, 0, time.UTC)
			if explicitEnd {
				endTime = time.Date(endTime.Year(), endTime.Month(), endTime.Day(), 0, 0, 0, 0, time.UTC)
			} else {
				// RFC 5545 §3.6.1: an all-day VEVENT with no DTEND/DURATION has
				// an implicit duration of one day.
				endTime = startTime.AddDate(0, 0, 1)
			}
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

	var conferenceURI string
	if prop := ve.Props.Get("CONFERENCE"); prop != nil {
		conferenceURI = prop.Value
	}

	var geo string
	if prop := ve.Props.Get(ical.PropGeo); prop != nil {
		geo = prop.Value
	}

	categories := parseCategories(ve)
	exdates := parseDateListFromProps(ve.Props, ical.PropExceptionDates, dtstartZone(ve.Props))
	rdates := parseDateListFromProps(ve.Props, ical.PropRecurrenceDates, dtstartZone(ve.Props))

	recurrenceID, err := parseRecurrenceID(ve.Props)
	if err != nil {
		return event.Event{}, nil, err
	}

	var dtstamp string
	if prop := ve.Props.Get(ical.PropDateTimeStamp); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			dtstamp = t.UTC().Format(time.RFC3339)
		}
	}

	// VALARM children
	var alarms []model.Alarm
	var childWarnings []string
	for _, child := range ve.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		alarm, w := parseAlarm(child)
		if w != "" {
			// Name the owning record: the dropped alarm leaves no other trace.
			childWarnings = append(childWarnings, fmt.Sprintf("event %q: %s", uid, w))
		}
		if alarm.TriggerValue != "" {
			alarms = append(alarms, alarm)
		}
	}

	// ATTENDEE + ORGANIZER
	attendees, attendeeWarnings := parseAttendees(ve)
	for _, w := range attendeeWarnings {
		// Name the owning record: the clamp leaves no other trace.
		childWarnings = append(childWarnings, fmt.Sprintf("event %q: %s", uid, w))
	}

	// ATTACH, COMMENT, RELATED-TO
	attachments, err := parseAttachmentsFromProps(ve.Props)
	if err != nil {
		return event.Event{}, nil, err
	}
	comments := parseCommentsFromProps(ve.Props)
	contacts := parseContactsFromProps(ve.Props)
	resources := parseResourcesFromProps(ve.Props)
	relations := parseRelationsFromProps(ve.Props)

	xprops := extractXPropertiesWithSet(ve.Props, handledEventProps)
	if remote && badDTENDRaw != nil {
		// The server's DTEND failed to parse here but parses on the
		// server (a non-IANA TZID is the usual cause). Store the raw
		// property, so export returns the server value instead of the
		// fabricated span (issue #567).
		params := "{}"
		if len(badDTENDRaw.Params) > 0 {
			if b, err := json.Marshal(badDTENDRaw.Params); err == nil {
				params = string(b)
			}
		}
		xprops = append(xprops, model.XProperty{
			Name:   xpropOriginalDTEND,
			Value:  badDTENDRaw.Value,
			Params: params,
		})
	}
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
		ConferenceURI:  conferenceURI,
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
		XProperties:    xprops,
	}, append(childWarnings, dtendWarnings...), nil
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
			cats = append(cats, list...)
		}
	}
	// Join with comma-escaping so a category value that itself contains a
	// comma (e.g. "Foo, Bar") stays a single value through the in-memory
	// comma-joined representation instead of fragmenting on round-trip.
	return timeutil.JoinCategoryList(cats)
}

// handledEventProps is the set of property names explicitly parsed by eventFromVEvent.
var handledEventProps = map[string]bool{
	ical.PropUID: true, ical.PropSummary: true, ical.PropDescription: true,
	ical.PropLocation: true, ical.PropDateTimeStart: true, ical.PropDateTimeEnd: true,
	ical.PropDuration: true, ical.PropRecurrenceRule: true, ical.PropStatus: true,
	ical.PropTransparency: true, "SEQUENCE": true, ical.PropPriority: true,
	ical.PropClass: true, ical.PropURL: true, "CONFERENCE": true, ical.PropGeo: true,
	ical.PropCategories: true, ical.PropExceptionDates: true,
	ical.PropRecurrenceDates: true, ical.PropRecurrenceID: true,
	ical.PropDateTimeStamp: true, ical.PropCreated: true, ical.PropLastModified: true,
	ical.PropAttach: true, ical.PropComment: true, ical.PropContact: true,
	ical.PropResources: true, ical.PropRelatedTo: true,
	ical.PropAttendee: true, ical.PropOrganizer: true,
}
