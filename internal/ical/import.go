package ical

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/freebusy"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TimezoneData holds a serialized VTIMEZONE component extracted during import.
type TimezoneData struct {
	TZID string
	Data string // serialized VTIMEZONE component
}

type ImportResult struct {
	Events    []event.Event
	Todos     []todo.Todo
	Journals  []journal.Journal
	FreeBusy  []freebusy.Result
	Timezones []TimezoneData
	Warnings  []string
	// SkippedComponents counts VEVENT/VTODO/VJOURNAL components that failed
	// to parse and were dropped (each also recorded in Warnings). Non-zero
	// means Events/Todos/Journals is an incomplete inventory of the input, so
	// absence-based reconciliation (e.g. override pruning) is unsafe.
	SkippedComponents int
}

const (
	maxImportBytes           = 8 << 20
	maxInlineAttachmentBytes = 1 << 20
)

var errImportLimitExceeded = errors.New("ical import exceeds configured limits")

func ImportFile(r io.Reader) (ImportResult, error) {
	var result ImportResult
	// skipComponent is the one place that defines what "the parser dropped a
	// persistable component" means: the warning for the user plus the count
	// that disables absence-based reconciliation downstream (see
	// SkippedComponents). Every VEVENT/VTODO/VJOURNAL failure path must go
	// through it.
	skipComponent := func(kind string, err error) {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", kind, err))
		result.SkippedComponents++
	}
	data, err := io.ReadAll(io.LimitReader(r, maxImportBytes+1))
	if err != nil {
		return result, fmt.Errorf("read ical: %w", err)
	}
	if len(data) > maxImportBytes {
		return result, fmt.Errorf("ical payload exceeds %d bytes", maxImportBytes)
	}

	dec := ical.NewDecoder(bytes.NewReader(data))

	for {
		cal, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, fmt.Errorf("decode ical: %w", err)
		}

		// Build timezone map from VTIMEZONE components.
		tzMap := buildTZMap(cal)

		// Extract and serialize VTIMEZONE components for storage.
		for _, child := range cal.Children {
			if child.Name != ical.CompTimezone {
				continue
			}
			tzid := compPropText(child, ical.PropTimezoneID)
			if tzid == "" {
				continue
			}
			var buf bytes.Buffer
			enc := ical.NewEncoder(&buf)
			// Wrap in a minimal calendar for encoding. go-ical's encoder
			// rejects a VCALENDAR that is missing the mandatory PRODID and
			// VERSION properties, so set them; otherwise Encode fails and the
			// VTIMEZONE block is silently dropped from result.Timezones.
			tmpCal := ical.NewCalendar()
			tmpCal.Props.SetText(ical.PropVersion, "2.0")
			tmpCal.Props.SetText(ical.PropProductID, ProductID)
			tmpCal.Children = append(tmpCal.Children, child)
			if err := enc.Encode(tmpCal); err == nil {
				// Extract just the VTIMEZONE block from the encoded output.
				encoded := buf.String()
				if start := strings.Index(encoded, "BEGIN:VTIMEZONE"); start >= 0 {
					if end := strings.Index(encoded[start:], "END:VTIMEZONE"); end >= 0 {
						vtData := encoded[start : start+end+len("END:VTIMEZONE\r\n")]
						result.Timezones = append(result.Timezones, TimezoneData{
							TZID: tzid,
							Data: vtData,
						})
					}
				}
			}
		}

		skipped := make(map[string]int)
		for _, child := range cal.Children {
			switch child.Name {
			case ical.CompEvent:
				vevent := ical.Event{Component: child}
				resolveComponentTZIDs(child, tzMap)
				e, warns, err := eventFromVEvent(vevent)
				if err != nil {
					if errors.Is(err, errImportLimitExceeded) {
						return result, err
					}
					skipComponent("VEVENT", err)
					continue
				}
				result.Warnings = append(result.Warnings, warns...)
				result.Events = append(result.Events, e)
			case ical.CompToDo:
				resolveComponentTZIDs(child, tzMap)
				t, warns, err := todoFromVTodo(child)
				if err != nil {
					if errors.Is(err, errImportLimitExceeded) {
						return result, err
					}
					skipComponent("VTODO", err)
					continue
				}
				result.Warnings = append(result.Warnings, warns...)
				result.Todos = append(result.Todos, t)
			case ical.CompJournal:
				resolveComponentTZIDs(child, tzMap)
				j, warns, err := journalFromVJournal(child)
				if err != nil {
					if errors.Is(err, errImportLimitExceeded) {
						return result, err
					}
					skipComponent("VJOURNAL", err)
					continue
				}
				result.Warnings = append(result.Warnings, warns...)
				result.Journals = append(result.Journals, j)
			case ical.CompFreeBusy:
				resolveComponentTZIDs(child, tzMap)
				fb, err := freebusy.ParseComponent(child)
				if err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("VFREEBUSY: %v", err))
					continue
				}
				result.FreeBusy = append(result.FreeBusy, fb)
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

	var todoWarnings []string
	dueDate, w := parseDateProp(props, "todo", ical.PropDue, uid)
	if w != "" {
		todoWarnings = append(todoWarnings, w)
	}
	startDate, w := parseDateProp(props, "todo", ical.PropDateTimeStart, uid)
	if w != "" {
		todoWarnings = append(todoWarnings, w)
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
	attendees := parseAttendeesFromProps(props)

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
		XProperties:     extractXPropertiesWithSet(props, handledTodoProps),
	}, append(alarmWarnings, todoWarnings...), nil
}

// parseDateProp formats a component's date/date-time property for storage.
// It returns a warning rather than a silent discard of an unparseable value.
// A dropped DUE or DTSTART is invisible data loss. The record disappears from
// date views. Any alarms lose their anchor. The next export re-emits the
// component with no property. An empty first return means the
// property was absent (no warning) or unusable (warning set).
func parseDateProp(props ical.Props, kind, name, uid string) (string, string) {
	prop := props.Get(name)
	if prop == nil {
		return "", ""
	}
	t, err := prop.DateTime(nil)
	if err != nil || t.IsZero() {
		return "", fmt.Sprintf("%s %q: unparseable %s %q; dropped", kind, uid, name, prop.Value)
	}
	// A bare date (VALUE=DATE, "YYYYMMDD") is a calendar date, not an instant.
	if len(prop.Value) == 8 {
		return t.Format("2006-01-02"), ""
	}
	return t.UTC().Format(time.RFC3339), ""
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
	var dtendWarnings []string
	explicitEnd := false
	badDTEND := false
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
			dtendWarnings = append(dtendWarnings, fmt.Sprintf(
				"event %q: unparseable DTEND %q; ignored, falling back to DURATION or the default span", uid, prop.Value))
		}
	}
	if endTime.IsZero() {
		if prop := ve.Props.Get(ical.PropDuration); prop != nil && duration.Validate(prop.Value) == nil {
			durationValue = prop.Value
			endTime = addDuration(startTime, prop.Value)
			explicitEnd = true
		} else if prop != nil {
			// Malformed DURATION (go-ical stores the raw value without validating).
			// Fall back to a 1h span and drop the bad value so it is neither
			// persisted nor re-exported.
			endTime = startTime.Add(time.Hour)
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
	var alarmWarnings []string
	for _, child := range ve.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		alarm, w := parseAlarm(child)
		if w != "" {
			// Name the owning record: the dropped alarm leaves no other trace.
			alarmWarnings = append(alarmWarnings, fmt.Sprintf("event %q: %s", uid, w))
		}
		if alarm.TriggerValue != "" {
			alarms = append(alarms, alarm)
		}
	}

	// ATTENDEE + ORGANIZER
	attendees := parseAttendees(ve)

	// ATTACH, COMMENT, RELATED-TO
	attachments, err := parseAttachmentsFromProps(ve.Props)
	if err != nil {
		return event.Event{}, nil, err
	}
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
		XProperties:    extractXPropertiesWithSet(ve.Props, handledEventProps),
	}, append(alarmWarnings, dtendWarnings...), nil
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

// parseAlarm extracts a model.Alarm from a VALARM component.
// The second return value is a warning string (empty if no issues). A VALARM
// with several problems reports all of them. Each one changes what the alarm
// does in silence. The user needs to see every reason. An unsupported ACTION
// is the one early exit. It drops the whole alarm and stops the parse.
func parseAlarm(comp *ical.Component) (model.Alarm, string) {
	alarm := model.Alarm{Action: model.DefaultAlarmAction, Related: model.DefaultAlarmRelated}
	var warns []string

	if prop := comp.Props.Get(ical.PropAction); prop != nil {
		// Google Calendar emits ACTION:NONE as a "no reminder" sentinel.
		// An unsupported action must not reach the alarm tables (issue
		// #575, see model.ValidAlarmAction). Every later push omits the
		// VALARM, so Google can re-apply its default reminders. That loss
		// is smaller than a sync that never converges.
		switch action := strings.ToUpper(strings.TrimSpace(prop.Value)); {
		case action == "":
			// Keep the DISPLAY default. The service write boundary
			// (model.PrepareAlarmForWrite) fills the same default, and
			// a reminder must not vanish over an empty value.
		case !model.ValidAlarmAction(action):
			warns = append(warns, fmt.Sprintf("VALARM ACTION %q: unsupported action; alarm dropped", action))
			return model.Alarm{}, strings.Join(warns, "; ")
		default:
			alarm.Action = action
		}
	}
	if prop := comp.Props.Get(ical.PropTrigger); prop != nil {
		tv := prop.Value
		tzid := prop.Params.Get(ical.ParamTimezoneID)
		// Validate the trigger: one arm per value SHAPE, each format parsed
		// exactly once, with TZID consulted only where it matters (the
		// compact-floating form). Keep the accepted set in lockstep with
		// model.ParseAbsoluteTime — that function defines what a stored
		// absolute trigger MEANS, so a form accepted here but not there
		// would store alarms the fire path cannot read.
		isDuration := duration.Validate(tv) == nil
		valid := isDuration
		if !valid {
			if _, err := time.Parse("20060102T150405Z", tv); err == nil {
				// Already compact UTC — the canonical stored form.
				valid = true
			} else if t, err := time.Parse(time.RFC3339, tv); err == nil {
				// RFC 3339 carries its own offset (a TZID param, if present,
				// is redundant): normalize to compact UTC so a stored
				// absolute trigger has one encoding. Safe because the offset
				// is explicit — this is NOT the floating-value normalization
				// that was reverted (issue #572).
				tv = t.UTC().Format("20060102T150405Z")
				valid = true
			} else if t, err := time.Parse("20060102T150405", tv); err == nil {
				// Compact floating: resolve through the TZID when it loads;
				// otherwise keep the raw floating value, to be resolved
				// against the record's timezone at fire time.
				if tzid != "" {
					if loc, lerr := time.LoadLocation(tzid); lerr == nil {
						t = time.Date(t.Year(), t.Month(), t.Day(),
							t.Hour(), t.Minute(), t.Second(), 0, loc)
						tv = t.UTC().Format("20060102T150405Z")
					} else {
						warns = append(warns, fmt.Sprintf("VALARM TRIGGER TZID=%s: unknown timezone, treating as floating", tzid))
					}
				}
				valid = true
			}
		}
		if valid {
			alarm.TriggerValue = tv
		} else {
			// Unparseable (or empty) TRIGGER: leave TriggerValue empty so
			// the caller's `TriggerValue != ""` gate drops the alarm, and
			// warn.
			//
			// Preserving the raw value here looks like it protects round-trip
			// fidelity, but it cannot. The value is not expressible as valid
			// iCal, so the next push either emits a VALARM strict servers
			// reject with 400 — wedging the whole resource — or omits it and
			// deletes the alarm from the server anyway. Preserving only moves
			// that loss from "announced at import" to "silent at the next
			// push", and in the meantime stores an alarm every trigger-time
			// helper refuses while the CLI and TUI display it as an armed
			// reminder the alarm editor will not even open. Losing it loudly
			// is the honest trade.
			warns = append(warns, fmt.Sprintf("VALARM TRIGGER: unparseable value %q; alarm dropped (it could never fire)", tv))
		}
		// RELATED only means something on a duration trigger (it picks the
		// anchor the offset applies to). RFC 5545 §3.8.6.3's trigabs
		// production forbids RELATED on an absolute trigger, so export can
		// never emit it — a stored one would be junk that never round-trips
		// (push+pull resets it to START) and that silently resurfaces if the
		// user later switches the trigger to a duration. Keep the default
		// "START" for absolute triggers.
		if rel := strings.TrimSpace(prop.Params.Get("RELATED")); rel != "" && isDuration {
			// Same failure class as an unsupported ACTION (issue #575,
			// see model.ValidAlarmRelated).
			if up := strings.ToUpper(rel); model.ValidAlarmRelated(up) {
				alarm.Related = up
			} else {
				warns = append(warns, fmt.Sprintf("VALARM TRIGGER RELATED=%q: unsupported value, using START", rel))
			}
		}
	} else {
		// RFC 5545 requires TRIGGER. Without it the caller's gate drops
		// the alarm, and the drop must not be silent.
		warns = append(warns, "VALARM TRIGGER: missing; alarm dropped (it could never fire)")
	}
	if prop := comp.Props.Get(ical.PropDescription); prop != nil {
		alarm.Description = prop.Value
	}
	if prop := comp.Props.Get(ical.PropSummary); prop != nil {
		alarm.Summary = prop.Value
	}
	repeatZero := false
	clampedFrom := 0
	if prop := comp.Props.Get("REPEAT"); prop != nil {
		// Clamp: REPEAT expands into per-trigger state in the check loop,
		// so an absurd imported value must not hang or OOM every check.
		// The clamp warning waits until after the pairing rule below.
		// Without the wait, one report could claim the clamp survived
		// and the repeat was dropped at the same time.
		v, err := strconv.Atoi(strings.TrimSpace(prop.Value))
		switch {
		case err != nil || v < 0:
			warns = append(warns, fmt.Sprintf("VALARM REPEAT: invalid value %q; ignored", prop.Value))
		case v > model.MaxAlarmRepeat:
			alarm.Repeat = model.MaxAlarmRepeat
			clampedFrom = v
		case v > 0:
			alarm.Repeat = v
		default:
			repeatZero = true // a valid "no repeats", not a defect
		}
	}
	if prop := comp.Props.Get(ical.PropDuration); prop != nil {
		// A malformed DURATION pushes verbatim, and a strict CalDAV
		// server rejects the whole resource with 400. See
		// model.ValidAlarmDuration for the value rule.
		if v := strings.TrimSpace(prop.Value); model.ValidAlarmDuration(v) {
			alarm.Duration = v
		} else if alarm.Repeat > 0 {
			// One defect, one warning: the REPEAT is unusable without
			// its interval. Name both drops here, so the pairing check
			// below finds a complete pair and adds no second warning.
			alarm.Repeat = 0
			warns = append(warns, fmt.Sprintf("VALARM DURATION: invalid value %q; dropped with its REPEAT", prop.Value))
		} else {
			warns = append(warns, fmt.Sprintf("VALARM DURATION: invalid value %q; dropped", prop.Value))
		}
	}
	// RFC 5545 §3.8.6.3 requires REPEAT and DURATION together. An unpaired
	// value cannot round-trip: buildValarm omits it, and the next pull then
	// deletes and recreates the alarm row, which loses the alarm state.
	// Clear the incomplete pair and name the dropped side. An explicit
	// REPEAT:0 with a DURATION is legal iCal. The row cannot store
	// "repeats disabled" apart from "REPEAT absent", so that pair cannot
	// round-trip either. It gets its own accurate message.
	switch {
	case repeatZero && alarm.Duration != "":
		alarm.Duration = ""
		warns = append(warns, "VALARM REPEAT:0: repeats disabled; DURATION dropped")
	case !alarm.RepeatPaired():
		dropped := "REPEAT"
		if alarm.Duration != "" {
			dropped = "DURATION"
		}
		alarm.Repeat = 0
		alarm.Duration = ""
		warns = append(warns, fmt.Sprintf("VALARM: REPEAT and DURATION must appear together; %s dropped", dropped))
	}
	if clampedFrom > 0 && alarm.Repeat > 0 {
		warns = append(warns, fmt.Sprintf("VALARM REPEAT: value %d clamped to %d", clampedFrom, model.MaxAlarmRepeat))
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

	// ATTACH (sound for AUDIO alarms): either a URI or an inline BASE64 blob.
	if prop := comp.Props.Get(ical.PropAttach); prop != nil {
		if prop.Params.Get("ENCODING") == "BASE64" {
			if data, err := decodeInlineAttachment(prop.Value); err != nil {
				warns = append(warns, fmt.Sprintf("VALARM ATTACH: %v", err))
			} else {
				alarm.AttachBinary = data
				alarm.AttachFmtType = prop.Params.Get("FMTTYPE")
			}
		} else {
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

	// X-* extension properties — preserved for round-trip fidelity only.
	// Normalize to a non-nil slice: for ReplaceAlarms, non-nil means "this
	// is the complete X-prop set" (empty = remote cleared them), while nil
	// means "caller has no X-prop knowledge, keep stored rows".
	alarm.XProperties = extractXPropertiesWithSet(comp.Props, nil)
	if alarm.XProperties == nil {
		alarm.XProperties = []model.XProperty{}
	}

	return alarm, strings.Join(warns, "; ")
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

// parseRecurrenceID extracts a RECURRENCE-ID as an RFC 3339 UTC string, or ""
// when the property is absent. A present-but-unparseable value fails the
// component. A degrade to "" would import the override as the series
// master. That would clobber the real master row and corrupt override
// reconciliation.
func parseRecurrenceID(props ical.Props) (string, error) {
	prop := props.Get(ical.PropRecurrenceID)
	if prop == nil {
		return "", nil
	}
	t, err := prop.DateTime(nil)
	if err != nil || t.IsZero() {
		return "", fmt.Errorf("unparseable RECURRENCE-ID %q", prop.Value)
	}
	return t.UTC().Format(time.RFC3339), nil
}

func parseCategoriesFromProps(props ical.Props) string {
	var cats []string
	for _, prop := range props.Values(ical.PropCategories) {
		if list, err := prop.TextList(); err == nil {
			cats = append(cats, list...)
		}
	}
	return timeutil.JoinCategoryList(cats)
}

// dtstartZone resolves the component's anchor zone for TZID-less DATE-TIME
// values elsewhere in the component. The anchor is the IANA TZID on DTSTART
// (or DUE for a VTODO with no DTSTART). Returns nil when the anchor is absent,
// floating, UTC, or date-only. Those components have no zone to inherit.
// TZID-less values then keep their UTC/floating read.
func dtstartZone(props ical.Props) *time.Location {
	for _, name := range []string{ical.PropDateTimeStart, ical.PropDue} {
		prop := props.Get(name)
		if prop == nil {
			continue
		}
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			if loc, err := time.LoadLocation(tzid); err == nil {
				return loc
			}
		}
		return nil
	}
	return nil
}

func parseDateListFromProps(props ical.Props, propName string, fallback *time.Location) string {
	var dates []string
	for _, prop := range props.Values(propName) {
		var loc *time.Location
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			if loaded, err := time.LoadLocation(tzid); err == nil {
				loc = loaded
			} else {
				// Unresolvable TZID (a private or Windows name that survived
				// resolveComponentTZIDs): reading the wall clock as UTC skews
				// the instant by the zone offset, so the EXDATE never matches
				// the occurrence it excludes and a server-cancelled instance
				// keeps rendering locally. The component's DTSTART zone is the
				// best-effort locality.
				loc = fallback
			}
		} else {
			// No TZID: a floating local time (RFC 5545 §3.3.5). For a
			// zone-anchored component the wall clock is local to the DTSTART
			// zone (Google transiently emits EXDATEs in this form), not UTC.
			// Z-suffixed values never reach the loc branch — ParseInLocation
			// rejects the trailing Z — and keep their UTC reading below.
			loc = fallback
		}
		parts := strings.Split(prop.Value, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// RDATE;VALUE=PERIOD values are "start/end" or "start/duration"
			// (RFC 5545 §3.8.5.2). Keep the start instant; the period's
			// duration is not represented in the comma-separated RDATE store.
			if i := strings.IndexByte(p, '/'); i >= 0 {
				p = p[:i]
			}
			if strings.EqualFold(prop.Params.Get("VALUE"), "DATE") {
				if t, err := time.Parse("20060102", p); err == nil {
					dates = append(dates, t.Format("2006-01-02"))
				}
				continue
			}
			if loc != nil {
				if t, err := time.ParseInLocation("20060102T150405", p, loc); err == nil {
					dates = append(dates, t.UTC().Format(time.RFC3339))
					continue
				}
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

// attendeeFromProp extracts a model.Attendee from an iCal ATTENDEE property.
// All RFC 5545 parameters are included.
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
// string. It strips the "mailto:" prefix and quotes around each value.
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

// decodeInlineAttachment decodes a BASE64 ATTACH value and enforces the inline
// size limit. The length is checked before decode so a deliberately oversized
// payload is rejected without a large buffer allocation.
func decodeInlineAttachment(value string) ([]byte, error) {
	if base64.StdEncoding.DecodedLen(len(value)) > maxInlineAttachmentBytes {
		return nil, fmt.Errorf("%w: inline attachment exceeds %d bytes", errImportLimitExceeded, maxInlineAttachmentBytes)
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode inline attachment: %w", err)
	}
	if len(data) > maxInlineAttachmentBytes {
		return nil, fmt.Errorf("%w: inline attachment exceeds %d bytes", errImportLimitExceeded, maxInlineAttachmentBytes)
	}
	return data, nil
}

func parseAttachmentsFromProps(props ical.Props) ([]model.Attachment, error) {
	var out []model.Attachment
	for _, prop := range props.Values(ical.PropAttach) {
		fmttype := prop.Params.Get("FMTTYPE")
		if prop.Params.Get("ENCODING") == "BASE64" {
			data, err := decodeInlineAttachment(prop.Value)
			if err != nil {
				return nil, err
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
	return out, nil
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

func journalFromVJournal(comp *ical.Component) (journal.Journal, []string, error) {
	props := comp.Props

	uid := propText(props, ical.PropUID)
	if uid == "" {
		return journal.Journal{}, nil, fmt.Errorf("missing UID")
	}

	summary := propText(props, ical.PropSummary)

	// VJOURNAL can have multiple DESCRIPTION properties (RFC 5545). Keep each
	// one in the Descriptions slice for round-trip fidelity, and join them into
	// the single DB-backed Description field used for display and search.
	var descriptions []string
	for _, prop := range props.Values(ical.PropDescription) {
		text, err := prop.Text()
		if err != nil {
			text = prop.Value
		}
		if text != "" {
			descriptions = append(descriptions, text)
		}
	}
	description := strings.Join(descriptions, "\n\n")

	startDate, startWarn := parseDateProp(props, "journal", ical.PropDateTimeStart, uid)
	var journalWarnings []string
	if startWarn != "" {
		journalWarnings = append(journalWarnings, startWarn)
	}

	status := propTextOr(props, ical.PropStatus, "FINAL")
	class := propTextOr(props, ical.PropClass, "PUBLIC")

	var sequence int64
	if prop := props.Get("SEQUENCE"); prop != nil {
		if v, err := strconv.ParseInt(prop.Value, 10, 64); err == nil {
			sequence = v
		}
	}

	url := propText(props, ical.PropURL)

	var timezone string
	var journalFloating bool
	if prop := props.Get(ical.PropDateTimeStart); prop != nil {
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			timezone = tzid
		} else if len(prop.Value) > 8 && !strings.HasSuffix(prop.Value, "Z") {
			journalFloating = true
		}
	}

	categories := parseCategoriesFromProps(props)
	exdates := parseDateListFromProps(props, ical.PropExceptionDates, dtstartZone(props))
	rdates := parseDateListFromProps(props, ical.PropRecurrenceDates, dtstartZone(props))
	var rrule string
	if prop := props.Get(ical.PropRecurrenceRule); prop != nil {
		rrule = prop.Value
	}

	recurrenceID, err := parseRecurrenceID(props)
	if err != nil {
		return journal.Journal{}, nil, err
	}

	var dtstamp string
	if prop := props.Get(ical.PropDateTimeStamp); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			dtstamp = t.UTC().Format(time.RFC3339)
		}
	}

	// ATTENDEE + ORGANIZER
	attendees := parseAttendeesFromProps(props)

	// ATTACH, COMMENT, CONTACT, RELATED-TO
	attachments, err := parseAttachmentsFromProps(props)
	if err != nil {
		return journal.Journal{}, nil, err
	}
	comments := parseCommentsFromProps(props)
	contacts := parseContactsFromProps(props)
	relations := parseRelationsFromProps(props)

	return journal.Journal{
		UID:            uid,
		Summary:        summary,
		Description:    description,
		Descriptions:   descriptions,
		StartDate:      startDate,
		Status:         strings.ToUpper(status),
		Class:          strings.ToUpper(class),
		URL:            url,
		Categories:     categories,
		RecurrenceRule: rrule,
		Timezone:       floatingOrTZ(journalFloating, timezone),
		Sequence:       sequence,
		ExDates:        exdates,
		RDates:         rdates,
		RecurrenceID:   recurrenceID,
		DtStamp:        dtstamp,
		Attendees:      attendees,
		Attachments:    attachments,
		Comments:       comments,
		Contacts:       contacts,
		XProperties:    extractXPropertiesWithSet(props, handledJournalProps),
		Relations:      relations,
	}, journalWarnings, nil
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

// handledJournalProps is the set of property names explicitly parsed by journalFromVJournal.
var handledJournalProps = map[string]bool{
	ical.PropUID: true, ical.PropSummary: true, ical.PropDescription: true,
	ical.PropDateTimeStart: true, ical.PropRecurrenceRule: true, ical.PropStatus: true,
	"SEQUENCE": true, ical.PropClass: true, ical.PropURL: true,
	ical.PropCategories: true, ical.PropExceptionDates: true,
	ical.PropRecurrenceDates: true, ical.PropRecurrenceID: true,
	ical.PropDateTimeStamp: true, ical.PropCreated: true, ical.PropLastModified: true,
	ical.PropAttach: true, ical.PropComment: true, ical.PropContact: true,
	ical.PropRelatedTo: true,
	ical.PropAttendee:  true, ical.PropOrganizer: true,
}

// extractXPropertiesWithSet collects properties not in the handled set.
// If handled is nil, only X-* prefixed properties are captured.
func extractXPropertiesWithSet(props ical.Props, handled map[string]bool) []model.XProperty {
	var out []model.XProperty

	// Iterate property names in sorted order for deterministic output.
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		isXProp := strings.HasPrefix(name, "X-")
		if !isXProp && (handled == nil || handled[name]) {
			continue
		}
		if isLibicalDiagnosticProp(name) {
			continue
		}
		for _, prop := range props[name] {
			params := "{}"
			if len(prop.Params) > 0 {
				if b, err := json.Marshal(prop.Params); err == nil {
					params = string(b)
				}
			}
			out = append(out, model.XProperty{
				Name:   name,
				Value:  prop.Value,
				Params: params,
			})
		}
	}
	return out
}

// isLibicalDiagnosticProp reports whether an X-property name is a libical
// internal diagnostic marker. libical writes X-LIC-ERROR / X-LIC-ERRORTYPE
// directly into the parsed property bag whenever it gives up on a malformed
// property. They look like X-properties to consumers but are really
// parser-state. They round-trip into our DB on import and back onto the
// wire on export. Strict CalDAV servers (Google) then reject the resource
// with HTTP 400. Drop them at both ends so they never reach the server.
func isLibicalDiagnosticProp(name string) bool {
	return strings.HasPrefix(name, "X-LIC-")
}
