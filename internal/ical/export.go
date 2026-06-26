package ical

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// ProductID is the PRODID value written into exported VCALENDAR objects.
// Override before calling ExportEvents or ExportTodos to customise.
var ProductID = "-//chroncal//chroncal//EN"

func ExportEvents(events []event.Event, calName string) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProductID)
	cal.Props.SetText("CALSCALE", "GREGORIAN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	// Emit VTIMEZONE components for all referenced timezones (RFC 5545 Section 3.6.5).
	seen := make(map[string]bool)
	for _, e := range events {
		if e.Timezone != "" && e.Timezone != "FLOATING" && !seen[e.Timezone] {
			seen[e.Timezone] = true
			if vtz, err := buildVTimezone(e.Timezone); err == nil {
				cal.Children = append(cal.Children, vtz)
			}
		}
	}

	for _, e := range events {
		vevent := ical.NewEvent()
		vevent.Props.SetText(ical.PropUID, e.UID)
		vevent.Props.SetText(ical.PropSummary, e.Title)

		if e.Description != "" {
			vevent.Props.SetText(ical.PropDescription, e.Description)
		}
		if e.Location != "" {
			vevent.Props.SetText(ical.PropLocation, e.Location)
		}

		// DTSTART / DTEND with optional timezone
		setEventTimes(vevent, e)

		if e.RecurrenceRule != "" {
			rruleProp := &ical.Prop{Name: ical.PropRecurrenceRule}
			rruleProp.Value = e.RecurrenceRule
			vevent.Props.Set(rruleProp)
		}

		// RFC 5545 properties
		vevent.Props.SetText(ical.PropStatus, e.Status)
		vevent.Props.SetText(ical.PropTransparency, e.Transp)

		seq := &ical.Prop{Name: "SEQUENCE"}
		seq.Value = strconv.FormatInt(e.Sequence, 10)
		vevent.Props.Set(seq)

		if e.Priority > 0 {
			p := &ical.Prop{Name: ical.PropPriority}
			p.Value = strconv.FormatInt(e.Priority, 10)
			vevent.Props.Set(p)
		}

		if e.Class != "" && e.Class != "PUBLIC" {
			vevent.Props.SetText(ical.PropClass, e.Class)
		}

		if e.URL != "" {
			p := &ical.Prop{Name: ical.PropURL}
			p.Value = e.URL
			vevent.Props.Set(p)
		}

		if e.ConferenceURI != "" {
			p := &ical.Prop{Name: "CONFERENCE"}
			p.Value = e.ConferenceURI
			p.Params.Set("VALUE", "URI")
			vevent.Props.Set(p)
		}

		if e.Categories != "" {
			// CATEGORIES is a comma-separated list of TEXT values.
			// SetTextList handles escaping within individual values and
			// uses unescaped commas as separators per RFC 5545 Section 3.8.1.2.
			catProp := &ical.Prop{Name: ical.PropCategories}
			catProp.SetTextList(e.ParseCategories())
			vevent.Props.Set(catProp)
		}

		// EXDATE
		emitDateListOnComponent(vevent.Component, ical.PropExceptionDates, e.ExDates)

		// RDATE
		emitDateListOnComponent(vevent.Component, ical.PropRecurrenceDates, e.RDates)

		// RECURRENCE-ID
		if e.RecurrenceID != "" {
			emitRecurrenceID(vevent.Props, e.RecurrenceID, e.AllDay)
		}

		if e.Geo != "" {
			p := &ical.Prop{Name: ical.PropGeo}
			p.Value = e.Geo
			vevent.Props.Set(p)
		}

		if e.DtStamp != "" {
			if ts, err := time.Parse(time.RFC3339, e.DtStamp); err == nil {
				vevent.Props.SetDateTime(ical.PropDateTimeStamp, ts.UTC())
			} else {
				vevent.Props.SetDateTime(ical.PropDateTimeStamp, e.UpdatedAt.UTC())
			}
		} else {
			vevent.Props.SetDateTime(ical.PropDateTimeStamp, e.UpdatedAt.UTC())
		}
		vevent.Props.SetDateTime(ical.PropCreated, e.CreatedAt.UTC())
		vevent.Props.SetDateTime(ical.PropLastModified, e.UpdatedAt.UTC())

		// ATTACH
		for _, att := range e.Attachments {
			emitAttachment(vevent.Props, att)
		}

		// COMMENT
		for _, c := range e.Comments {
			p := &ical.Prop{Name: ical.PropComment}
			p.SetText(c)
			vevent.Props.Add(p)
		}

		// CONTACT
		for _, c := range e.Contacts {
			p := &ical.Prop{Name: ical.PropContact}
			p.SetText(c)
			vevent.Props.Add(p)
		}

		// RESOURCES (comma-separated list, like CATEGORIES)
		if len(e.Resources) > 0 {
			resProp := &ical.Prop{Name: ical.PropResources}
			resProp.SetTextList(e.Resources)
			vevent.Props.Set(resProp)
		}

		// RELATED-TO
		for _, r := range e.Relations {
			p := &ical.Prop{Name: ical.PropRelatedTo, Params: make(ical.Params)}
			p.Value = r.RelUID
			if r.RelType != "" && r.RelType != "PARENT" {
				p.Params.Set("RELTYPE", r.RelType)
			}
			vevent.Props.Add(p)
		}

		// X-Properties (round-trip preservation)
		emitXProperties(vevent.Component, e.XProperties)

		// VALARM children
		for _, alarm := range e.Alarms {
			if alarm.Summary == "" && alarm.Action == "EMAIL" {
				alarm.Summary = e.Title
			}
			vevent.Children = append(vevent.Children, buildValarm(alarm))
		}

		// ATTENDEE / ORGANIZER
		for _, att := range e.Attendees {
			if att.Organizer {
				org := &ical.Prop{Name: ical.PropOrganizer, Params: make(ical.Params)}
				org.Value = "mailto:" + att.Email
				if att.Name != "" {
					org.Params.Set(ical.ParamCommonName, att.Name)
				}
				setOrganizerParams(org, att)
				vevent.Props.Set(org)
			}

			attendee := &ical.Prop{Name: ical.PropAttendee, Params: make(ical.Params)}
			attendee.Value = "mailto:" + att.Email
			if att.Name != "" {
				attendee.Params.Set(ical.ParamCommonName, att.Name)
			}
			attendee.Params.Set(ical.ParamParticipationStatus, att.RSVPStatus)
			attendee.Params.Set(ical.ParamRole, att.Role)
			setAttendeeParams(attendee, att)
			vevent.Props.Add(attendee)
		}

		cal.Children = append(cal.Children, vevent.Component)
	}

	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return nil, fmt.Errorf("encode ical: %w", err)
	}
	return buf.Bytes(), nil
}

// setPropDuration writes a DURATION property without VALUE=TEXT parameter.
func setPropDuration(vevent *ical.Event, dur string) {
	p := &ical.Prop{Name: ical.PropDuration}
	p.Value = dur
	vevent.Props.Set(p)
}

// setPropFloating writes a datetime property without TZID and without Z suffix
// (RFC 5545 floating time: local time in whatever timezone the viewer is in).
func setPropFloating(vevent *ical.Event, propName string, t time.Time) {
	p := &ical.Prop{Name: propName}
	p.Value = t.Format("20060102T150405")
	vevent.Props.Set(p)
}

func setEventTimes(vevent *ical.Event, e event.Event) {
	// RFC 5545 forbids both DTEND and DURATION on the same VEVENT.
	// When DurationValue is set (imported from .ics), emit DURATION instead of DTEND.
	useDuration := e.DurationValue != ""

	if e.AllDay {
		vevent.Props.SetDate(ical.PropDateTimeStart, allDayExportDate(e.StartTime, e.Timezone))
		if useDuration {
			setPropDuration(vevent, e.DurationValue)
		} else {
			vevent.Props.SetDate(ical.PropDateTimeEnd, allDayExportDate(e.EndTime, e.Timezone))
		}
	} else if e.Timezone == "FLOATING" {
		// Floating times are host-independent wall clocks. Import interprets
		// them as UTC, so export must emit the stored UTC wall clock (not
		// .Local(), which would shift the clock on non-UTC hosts).
		setPropFloating(vevent, ical.PropDateTimeStart, e.StartTime.UTC())
		if useDuration {
			setPropDuration(vevent, e.DurationValue)
		} else {
			setPropFloating(vevent, ical.PropDateTimeEnd, e.EndTime.UTC())
		}
	} else if e.Timezone != "" {
		loc, err := time.LoadLocation(e.Timezone)
		if err == nil {
			vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.In(loc))
			if prop := vevent.Props.Get(ical.PropDateTimeStart); prop != nil {
				prop.Params.Set(ical.ParamTimezoneID, e.Timezone)
			}
			if useDuration {
				setPropDuration(vevent, e.DurationValue)
			} else {
				vevent.Props.SetDateTime(ical.PropDateTimeEnd, e.EndTime.In(loc))
				if prop := vevent.Props.Get(ical.PropDateTimeEnd); prop != nil {
					prop.Params.Set(ical.ParamTimezoneID, e.Timezone)
				}
			}
		} else {
			vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.UTC())
			if useDuration {
				setPropDuration(vevent, e.DurationValue)
			} else {
				vevent.Props.SetDateTime(ical.PropDateTimeEnd, e.EndTime.UTC())
			}
		}
	} else {
		vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.UTC())
		if useDuration {
			setPropDuration(vevent, e.DurationValue)
		} else {
			vevent.Props.SetDateTime(ical.PropDateTimeEnd, e.EndTime.UTC())
		}
	}
}

func allDayExportDate(t time.Time, timezone string) time.Time {
	// A stored instant already at midnight UTC carries its calendar date
	// directly (the TUI stores all-day events as midnight UTC, regardless of
	// the event's Timezone). Converting it into another zone would shift the
	// date — e.g. 2026-04-15T00:00Z in America/New_York is 2026-04-14.
	if t.Location() == time.UTC && t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
		return t
	}
	if timezone != "" && timezone != "FLOATING" {
		if loc, err := time.LoadLocation(timezone); err == nil {
			return t.In(loc)
		}
	}
	if t.Location() != time.UTC {
		return t
	}
	if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
		return t.In(time.Local)
	}
	return t.UTC()
}

func ExportTodos(todos []todo.Todo, calName string) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProductID)
	cal.Props.SetText("CALSCALE", "GREGORIAN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	// Emit VTIMEZONE components for all referenced timezones.
	seen := make(map[string]bool)
	for _, t := range todos {
		if t.Timezone != "" && t.Timezone != "FLOATING" && !seen[t.Timezone] {
			seen[t.Timezone] = true
			if vtz, err := buildVTimezone(t.Timezone); err == nil {
				cal.Children = append(cal.Children, vtz)
			}
		}
	}

	for _, t := range todos {
		vtodo := ical.NewComponent(ical.CompToDo)

		vtodo.Props.SetText(ical.PropUID, t.UID)
		vtodo.Props.SetText(ical.PropSummary, t.Summary)

		if t.Description != "" {
			vtodo.Props.SetText(ical.PropDescription, t.Description)
		}
		if t.Location != "" {
			vtodo.Props.SetText(ical.PropLocation, t.Location)
		}

		// DUE or DTSTART+DURATION (with optional timezone)
		if t.DueDate != "" {
			if d, err := time.Parse("2006-01-02", t.DueDate); err == nil {
				vtodo.Props.SetDate(ical.PropDue, d)
			} else if due, err := time.Parse(time.RFC3339, t.DueDate); err == nil {
				if t.Timezone == "FLOATING" {
					p := &ical.Prop{Name: ical.PropDue}
					p.Value = due.UTC().Format("20060102T150405")
					vtodo.Props.Set(p)
				} else if t.Timezone != "" {
					if loc, lerr := time.LoadLocation(t.Timezone); lerr == nil {
						vtodo.Props.SetDateTime(ical.PropDue, due.In(loc))
						if p := vtodo.Props.Get(ical.PropDue); p != nil {
							p.Params.Set(ical.ParamTimezoneID, t.Timezone)
						}
					} else {
						vtodo.Props.SetDateTime(ical.PropDue, due.UTC())
					}
				} else {
					vtodo.Props.SetDateTime(ical.PropDue, due.UTC())
				}
			}
		}
		if t.StartDate != "" {
			if d, err := time.Parse("2006-01-02", t.StartDate); err == nil {
				vtodo.Props.SetDate(ical.PropDateTimeStart, d)
			} else if start, err := time.Parse(time.RFC3339, t.StartDate); err == nil {
				if t.Timezone == "FLOATING" {
					p := &ical.Prop{Name: ical.PropDateTimeStart}
					p.Value = start.UTC().Format("20060102T150405")
					vtodo.Props.Set(p)
				} else if t.Timezone != "" {
					if loc, lerr := time.LoadLocation(t.Timezone); lerr == nil {
						vtodo.Props.SetDateTime(ical.PropDateTimeStart, start.In(loc))
						if p := vtodo.Props.Get(ical.PropDateTimeStart); p != nil {
							p.Params.Set(ical.ParamTimezoneID, t.Timezone)
						}
					} else {
						vtodo.Props.SetDateTime(ical.PropDateTimeStart, start.UTC())
					}
				} else {
					vtodo.Props.SetDateTime(ical.PropDateTimeStart, start.UTC())
				}
			}
		}
		// RFC 5545 (and go-ical's encoder) only accept DURATION on a VTODO when
		// DTSTART is present and DUE is absent. A stored todo can violate this
		// (import enforces no mutual exclusion), and a single bad component makes
		// enc.Encode reject the whole calendar, dropping every todo. Drop the
		// conflicting DURATION instead so the rest of the batch still exports.
		if t.Duration != "" &&
			vtodo.Props.Get(ical.PropDue) == nil &&
			vtodo.Props.Get(ical.PropDateTimeStart) != nil {
			p := &ical.Prop{Name: ical.PropDuration}
			p.Value = t.Duration
			vtodo.Props.Set(p)
		}

		// Completion
		if t.CompletedAt != "" {
			if ca, err := time.Parse(time.RFC3339, t.CompletedAt); err == nil {
				vtodo.Props.SetDateTime(ical.PropCompleted, ca.UTC())
			}
		}
		if t.PercentComplete > 0 {
			p := &ical.Prop{Name: ical.PropPercentComplete}
			p.Value = strconv.FormatInt(t.PercentComplete, 10)
			vtodo.Props.Set(p)
		}

		vtodo.Props.SetText(ical.PropStatus, t.Status)

		seq := &ical.Prop{Name: "SEQUENCE"}
		seq.Value = strconv.FormatInt(t.Sequence, 10)
		vtodo.Props.Set(seq)

		if t.Priority > 0 {
			p := &ical.Prop{Name: ical.PropPriority}
			p.Value = strconv.FormatInt(t.Priority, 10)
			vtodo.Props.Set(p)
		}

		if t.Class != "" && t.Class != "PUBLIC" {
			vtodo.Props.SetText(ical.PropClass, t.Class)
		}

		if t.URL != "" {
			p := &ical.Prop{Name: ical.PropURL}
			p.Value = t.URL
			vtodo.Props.Set(p)
		}

		if t.Categories != "" {
			catProp := &ical.Prop{Name: ical.PropCategories}
			catProp.SetTextList(t.ParseCategories())
			vtodo.Props.Set(catProp)
		}

		if t.RecurrenceRule != "" {
			rruleProp := &ical.Prop{Name: ical.PropRecurrenceRule}
			rruleProp.Value = t.RecurrenceRule
			vtodo.Props.Set(rruleProp)
		}

		// Dates
		emitDateListOnComponent(vtodo, ical.PropExceptionDates, t.ExDates)
		emitDateListOnComponent(vtodo, ical.PropRecurrenceDates, t.RDates)

		if t.RecurrenceID != "" {
			// A VTODO is all-day when its recurrence anchor (DTSTART, else DUE)
			// is a date-only value; the RECURRENCE-ID type must match.
			anchor := t.StartDate
			if anchor == "" {
				anchor = t.DueDate
			}
			emitRecurrenceID(vtodo.Props, t.RecurrenceID, timeutil.IsDateOnly(anchor))
		}

		if t.Geo != "" {
			p := &ical.Prop{Name: ical.PropGeo}
			p.Value = t.Geo
			vtodo.Props.Set(p)
		}

		if t.DtStamp != "" {
			if ts, err := time.Parse(time.RFC3339, t.DtStamp); err == nil {
				vtodo.Props.SetDateTime(ical.PropDateTimeStamp, ts.UTC())
			} else {
				vtodo.Props.SetDateTime(ical.PropDateTimeStamp, t.UpdatedAt.UTC())
			}
		} else {
			vtodo.Props.SetDateTime(ical.PropDateTimeStamp, t.UpdatedAt.UTC())
		}
		vtodo.Props.SetDateTime(ical.PropCreated, t.CreatedAt.UTC())
		vtodo.Props.SetDateTime(ical.PropLastModified, t.UpdatedAt.UTC())

		// ATTACH
		for _, att := range t.Attachments {
			emitAttachment(vtodo.Props, att)
		}

		// COMMENT
		for _, c := range t.Comments {
			p := &ical.Prop{Name: ical.PropComment}
			p.SetText(c)
			vtodo.Props.Add(p)
		}

		// CONTACT
		for _, c := range t.Contacts {
			p := &ical.Prop{Name: ical.PropContact}
			p.SetText(c)
			vtodo.Props.Add(p)
		}

		// RESOURCES (comma-separated list, like CATEGORIES)
		if len(t.Resources) > 0 {
			resProp := &ical.Prop{Name: ical.PropResources}
			resProp.SetTextList(t.Resources)
			vtodo.Props.Set(resProp)
		}

		// RELATED-TO
		for _, r := range t.Relations {
			p := &ical.Prop{Name: ical.PropRelatedTo, Params: make(ical.Params)}
			p.Value = r.RelUID
			if r.RelType != "" && r.RelType != "PARENT" {
				p.Params.Set("RELTYPE", r.RelType)
			}
			vtodo.Props.Add(p)
		}

		// X-Properties (round-trip preservation)
		emitXProperties(vtodo, t.XProperties)

		// VALARM
		for _, alarm := range t.Alarms {
			if alarm.Summary == "" && alarm.Action == "EMAIL" {
				alarm.Summary = t.Summary
			}
			vtodo.Children = append(vtodo.Children, buildValarm(alarm))
		}

		// ATTENDEE / ORGANIZER
		for _, att := range t.Attendees {
			if att.Organizer {
				org := &ical.Prop{Name: ical.PropOrganizer, Params: make(ical.Params)}
				org.Value = "mailto:" + att.Email
				if att.Name != "" {
					org.Params.Set(ical.ParamCommonName, att.Name)
				}
				setOrganizerParams(org, att)
				vtodo.Props.Set(org)
			}
			attendee := &ical.Prop{Name: ical.PropAttendee, Params: make(ical.Params)}
			attendee.Value = "mailto:" + att.Email
			if att.Name != "" {
				attendee.Params.Set(ical.ParamCommonName, att.Name)
			}
			attendee.Params.Set(ical.ParamParticipationStatus, att.RSVPStatus)
			attendee.Params.Set(ical.ParamRole, att.Role)
			setAttendeeParams(attendee, att)
			vtodo.Props.Add(attendee)
		}

		cal.Children = append(cal.Children, vtodo)
	}

	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return nil, fmt.Errorf("encode ical: %w", err)
	}
	return buf.Bytes(), nil
}

// MergeCalendars combines two iCal byte streams into one VCALENDAR.
// It takes the header from the first and appends all components from both.
func MergeCalendars(a, b []byte) []byte {
	// Simple approach: strip END:VCALENDAR from a, strip BEGIN:VCALENDAR...VERSION:2.0 header from b
	aStr := strings.TrimRight(string(a), "\r\n")
	bStr := string(b)

	// Remove trailing END:VCALENDAR from a
	if idx := strings.LastIndex(aStr, "END:VCALENDAR"); idx >= 0 {
		aStr = aStr[:idx]
	}

	// Extract VTIMEZONE blocks from b that are not already in a, so they
	// are preserved when the header of b is stripped below.
	var extraTZ string
	for _, tzBlock := range extractVTIMEZONEBlocks(bStr) {
		if !strings.Contains(aStr, tzBlock) {
			extraTZ += tzBlock
		}
	}

	// Remove header from b: find the earliest component marker regardless of
	// type, so that a stream mixing VEVENT/VTODO/VJOURNAL does not lose the
	// components that appear before the first marker of a later-searched type.
	firstComp := -1
	for _, marker := range []string{"BEGIN:VEVENT", "BEGIN:VTODO", "BEGIN:VJOURNAL"} {
		if idx := strings.Index(bStr, marker); idx >= 0 {
			if firstComp < 0 || idx < firstComp {
				firstComp = idx
			}
		}
	}
	if firstComp >= 0 {
		bStr = bStr[firstComp:]
	}

	// Remove trailing END:VCALENDAR from b, then re-add it
	if idx := strings.LastIndex(bStr, "END:VCALENDAR"); idx >= 0 {
		bStr = bStr[:idx]
	}

	return []byte(aStr + extraTZ + bStr + "END:VCALENDAR\r\n")
}

// extractVTIMEZONEBlocks returns all BEGIN:VTIMEZONE...END:VTIMEZONE\r\n
// segments found in s.
func extractVTIMEZONEBlocks(s string) []string {
	var blocks []string
	rest := s
	for {
		start := strings.Index(rest, "BEGIN:VTIMEZONE")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start:], "END:VTIMEZONE")
		if end < 0 {
			break
		}
		// end is relative to start; include the "END:VTIMEZONE" tag plus a
		// trailing \r\n if present.
		blockEnd := start + end + len("END:VTIMEZONE")
		if blockEnd < len(rest) && rest[blockEnd] == '\r' {
			blockEnd++
		}
		if blockEnd < len(rest) && rest[blockEnd] == '\n' {
			blockEnd++
		}
		blocks = append(blocks, rest[start:blockEnd])
		rest = rest[blockEnd:]
	}
	return blocks
}

func emitDateListOnComponent(comp *ical.Component, propName, dates string) {
	if dates == "" {
		return
	}
	for _, ds := range strings.Split(dates, ",") {
		ds = strings.TrimSpace(ds)
		// Date-only values (YYYY-MM-DD) → emit as VALUE=DATE
		if t, err := time.Parse("2006-01-02", ds); err == nil {
			prop := &ical.Prop{Name: propName, Params: make(ical.Params)}
			prop.Params.Set("VALUE", "DATE")
			prop.Value = t.Format("20060102")
			comp.Props.Add(prop)
			continue
		}
		if t, err := time.Parse(time.RFC3339, ds); err == nil {
			prop := &ical.Prop{Name: propName, Params: make(ical.Params)}
			prop.SetDateTime(t.UTC())
			comp.Props.Add(prop)
		}
	}
}

// emitRecurrenceID writes RECURRENCE-ID onto props. recurrenceID is the stored
// RFC 3339 string. Per RFC 5545 the RECURRENCE-ID value type must match the
// master's DTSTART: when the component is all-day it is emitted as VALUE=DATE
// (YYYYMMDD), otherwise as a UTC DATE-TIME. A type mismatch prevents CalDAV
// servers from binding the override to its master.
func emitRecurrenceID(props ical.Props, recurrenceID string, allDay bool) {
	t, err := time.Parse(time.RFC3339, recurrenceID)
	if err != nil {
		return
	}
	if allDay {
		props.SetDate(ical.PropRecurrenceID, t.UTC())
	} else {
		props.SetDateTime(ical.PropRecurrenceID, t.UTC())
	}
}

func buildValarm(alarm model.Alarm) *ical.Component {
	valarm := ical.NewComponent(ical.CompAlarm)
	if alarm.UID != "" {
		valarm.Props.SetText(ical.PropUID, alarm.UID)
	}
	valarm.Props.SetText(ical.PropAction, alarm.Action)

	trigger := &ical.Prop{Name: ical.PropTrigger, Params: make(ical.Params)}
	trigger.Value = alarm.TriggerValue
	if alarm.TriggerValue != "" && (alarm.TriggerValue[0] == '-' || alarm.TriggerValue[0] == '+' || alarm.TriggerValue[0] == 'P') {
		trigger.Params.Set("VALUE", "DURATION")
	} else if alarm.TriggerValue != "" {
		trigger.Params.Set("VALUE", "DATE-TIME")
		// Normalize any legacy RFC 3339 values to iCal format.
		if t, err := time.Parse(time.RFC3339, alarm.TriggerValue); err == nil {
			trigger.Value = t.UTC().Format("20060102T150405Z")
		}
	}
	if alarm.Related == "END" {
		trigger.Params.Set("RELATED", "END")
	}
	valarm.Props.Set(trigger)

	if alarm.Description != "" {
		valarm.Props.SetText(ical.PropDescription, alarm.Description)
	}
	if alarm.Summary != "" {
		valarm.Props.SetText(ical.PropSummary, alarm.Summary)
	}
	// RFC 5545 §3.8.6.3: DURATION and REPEAT MUST appear together; emitting
	// either one without the other yields an invalid VALARM that strict CalDAV
	// servers (e.g. Google) reject with HTTP 400, blocking the whole resource.
	if alarm.Repeat > 0 && alarm.Duration != "" {
		p := &ical.Prop{Name: ical.PropDuration}
		p.Value = alarm.Duration
		valarm.Props.Set(p)
		p2 := &ical.Prop{Name: "REPEAT"}
		p2.Value = strconv.Itoa(alarm.Repeat)
		valarm.Props.Set(p2)
	}
	// ACKNOWLEDGED (RFC 9074) — round-trip only.
	if alarm.Acknowledged != "" {
		p := &ical.Prop{Name: "ACKNOWLEDGED", Params: make(ical.Params)}
		p.Value = alarm.Acknowledged
		// Normalize RFC 3339 to iCal UTC format.
		if t, err := time.Parse(time.RFC3339, alarm.Acknowledged); err == nil {
			p.Value = t.UTC().Format("20060102T150405Z")
		}
		valarm.Props.Set(p)
	}

	for _, att := range alarm.Attendees {
		p := &ical.Prop{Name: ical.PropAttendee, Params: make(ical.Params)}
		p.Value = "mailto:" + att.Email
		if att.Name != "" {
			p.Params.Set(ical.ParamCommonName, att.Name)
		}
		valarm.Props.Add(p)
	}

	// ATTACH (sound for AUDIO alarms only): inline BASE64 blob or URI.
	if alarm.Action == "AUDIO" && (len(alarm.AttachBinary) > 0 || alarm.AttachURI != "") {
		p := &ical.Prop{Name: ical.PropAttach, Params: make(ical.Params)}
		if len(alarm.AttachBinary) > 0 {
			p.Value = base64.StdEncoding.EncodeToString(alarm.AttachBinary)
			p.Params.Set("ENCODING", "BASE64")
			p.Params.Set("VALUE", "BINARY")
		} else {
			p.Value = alarm.AttachURI
		}
		if alarm.AttachFmtType != "" {
			p.Params.Set("FMTTYPE", alarm.AttachFmtType)
		}
		valarm.Props.Add(p)
	}

	emitXProperties(valarm, alarm.XProperties)

	return valarm
}

// buildVTimezone generates a VTIMEZONE component for the given IANA timezone ID.
// It probes the current year for DST transitions and emits STANDARD and DAYLIGHT
// sub-components as needed, satisfying RFC 5545 Section 3.6.5.
func buildVTimezone(tzID string) (*ical.Component, error) {
	loc, err := time.LoadLocation(tzID)
	if err != nil {
		return nil, err
	}

	vtz := ical.NewComponent("VTIMEZONE")
	tzidProp := &ical.Prop{Name: "TZID"}
	tzidProp.Value = tzID
	vtz.Props.Set(tzidProp)

	refYear := time.Now().Year()

	// Probe the start of each month to detect offset transitions.
	type transition struct {
		name       string
		offset     int // seconds east of UTC
		month      time.Month
		fromOffset int
	}

	jan := time.Date(refYear, 1, 1, 12, 0, 0, 0, loc)
	janName, janOffset := jan.Zone()

	var transitions []transition
	prevOffset := janOffset
	for m := time.February; m <= time.December; m++ {
		t := time.Date(refYear, m, 1, 12, 0, 0, 0, loc)
		name, offset := t.Zone()
		if offset != prevOffset {
			transitions = append(transitions, transition{
				name: name, offset: offset, month: m, fromOffset: prevOffset,
			})
			prevOffset = offset
		}
	}

	fmtOffset := func(secs int) string {
		sign := "+"
		if secs < 0 {
			sign = "-"
			secs = -secs
		}
		return fmt.Sprintf("%s%02d%02d", sign, secs/3600, (secs%3600)/60)
	}

	addSubComp := func(compName, tzName string, offset, fromOffset int, dtstart time.Time, rrule string) {
		comp := ical.NewComponent(compName)

		p := &ical.Prop{Name: ical.PropDateTimeStart}
		p.Value = dtstart.In(loc).Format("20060102T150405")
		comp.Props.Set(p)

		p = &ical.Prop{Name: "TZOFFSETFROM"}
		p.Value = fmtOffset(fromOffset)
		comp.Props.Set(p)

		p = &ical.Prop{Name: "TZOFFSETTO"}
		p.Value = fmtOffset(offset)
		comp.Props.Set(p)

		p = &ical.Prop{Name: "TZNAME"}
		p.Value = tzName
		comp.Props.Set(p)

		// A yearly RRULE makes the transition apply to every year rather than
		// the single export year, so events far in the past/future still
		// resolve the right offset from the embedded VTIMEZONE (RFC 5545
		// Section 3.6.5).
		if rrule != "" {
			p = &ical.Prop{Name: ical.PropRecurrenceRule}
			p.Value = rrule
			comp.Props.Set(p)
		}

		vtz.Children = append(vtz.Children, comp)
	}

	if len(transitions) == 0 {
		// No DST — single STANDARD component
		addSubComp("STANDARD", janName, janOffset, janOffset,
			time.Date(refYear, 1, 1, 0, 0, 0, 0, loc), "")
	} else {
		for _, tr := range transitions {
			// The transition was detected between month M-1 and M, but the
			// exact day could be anywhere in the prior month. Search backward.
			dtstart := findTransitionDay(loc, refYear, tr.month, tr.fromOffset)
			compName := "STANDARD"
			if tr.offset > tr.fromOffset {
				compName = "DAYLIGHT"
			}
			addSubComp(compName, tr.name, tr.offset, tr.fromOffset, dtstart,
				transitionRRULE(dtstart))
		}
	}

	return vtz, nil
}

// findTransitionDay finds the exact day when the UTC offset changes to a new
// value, searching backward from the start of the detected month into the
// previous month where the transition actually occurs.
func findTransitionDay(loc *time.Location, year int, detectedMonth time.Month, prevOffset int) time.Time {
	// Search from the previous month's first day through the detected month
	searchStart := time.Date(year, detectedMonth-1, 1, 3, 0, 0, 0, loc)
	if detectedMonth == time.January {
		searchStart = time.Date(year-1, time.December, 1, 3, 0, 0, 0, loc)
	}
	searchEnd := time.Date(year, detectedMonth, 1, 3, 0, 0, 0, loc)
	for d := searchStart; !d.After(searchEnd); d = d.AddDate(0, 0, 1) {
		_, offset := d.Zone()
		if offset != prevOffset {
			return d
		}
	}
	return searchEnd
}

// transitionRRULE builds a yearly RFC 5545 recurrence rule describing when a
// DST transition repeats, derived from the weekday-of-month of dtstart. Most
// IANA zones transition on a fixed ordinal weekday (e.g. "2nd Sunday of March"
// -> FREQ=YEARLY;BYMONTH=3;BYDAY=2SU). When the weekday is the last such
// weekday of the month, BYDAY uses -1 (e.g. last Sunday -> BYDAY=-1SU), which
// also matches the common European rule.
func transitionRRULE(dtstart time.Time) string {
	weekdays := [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	wd := weekdays[dtstart.Weekday()]
	month := int(dtstart.Month())
	// Last occurrence of this weekday in the month? (One week later spills
	// into the next month.)
	if dtstart.AddDate(0, 0, 7).Month() != dtstart.Month() {
		return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=-1%s", month, wd)
	}
	nth := (dtstart.Day()-1)/7 + 1
	return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", month, nth, wd)
}

// setAttendeeParams adds RFC 5545 ATTENDEE parameters beyond the base CN/PARTSTAT/ROLE.
func setAttendeeParams(prop *ical.Prop, att model.Attendee) {
	if att.CUType != "" && att.CUType != "INDIVIDUAL" {
		prop.Params.Set(ical.ParamCalendarUserType, att.CUType)
	}
	if att.RSVPRequested {
		prop.Params.Set(ical.ParamRSVP, "TRUE")
	}
	if att.SentBy != "" {
		prop.Params.Set(ical.ParamSentBy, "mailto:"+att.SentBy)
	}
	for _, v := range splitNonEmpty(att.DelegatedTo) {
		prop.Params.Add(ical.ParamDelegatedTo, "mailto:"+v)
	}
	for _, v := range splitNonEmpty(att.DelegatedFrom) {
		prop.Params.Add(ical.ParamDelegatedFrom, "mailto:"+v)
	}
	for _, v := range splitNonEmpty(att.Member) {
		prop.Params.Add(ical.ParamMember, "mailto:"+v)
	}
	if att.Dir != "" {
		prop.Params.Set(ical.ParamDir, att.Dir)
	}
	if att.Language != "" {
		prop.Params.Set(ical.ParamLanguage, att.Language)
	}
}

// setOrganizerParams adds applicable RFC 5545 parameters to an ORGANIZER property.
func setOrganizerParams(prop *ical.Prop, att model.Attendee) {
	if att.SentBy != "" {
		prop.Params.Set(ical.ParamSentBy, "mailto:"+att.SentBy)
	}
	if att.Dir != "" {
		prop.Params.Set(ical.ParamDir, att.Dir)
	}
	if att.Language != "" {
		prop.Params.Set(ical.ParamLanguage, att.Language)
	}
}

// splitNonEmpty splits a comma-separated string and returns non-empty trimmed values.
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// emitXProperties writes X-properties (and other unhandled properties) onto an
// iCal component for round-trip preservation. libical-internal annotations
// (X-LIC-ERROR / X-LIC-ERRORTYPE) are skipped: those are diagnostic markers
// emitted by libical when it encountered a parse error in the original
// payload, not real properties. Echoing them back to a CalDAV server (Google
// in particular) gets the whole resource rejected with HTTP 400.
func emitXProperties(comp *ical.Component, xprops []model.XProperty) {
	for _, xp := range xprops {
		if isLibicalDiagnosticProp(xp.Name) {
			continue
		}
		p := &ical.Prop{Name: xp.Name, Params: make(ical.Params)}
		p.Value = xp.Value
		if xp.Params != "" && xp.Params != "{}" {
			var params map[string][]string
			if err := json.Unmarshal([]byte(xp.Params), &params); err == nil {
				for k, vals := range params {
					for _, v := range vals {
						p.Params.Add(k, v)
					}
				}
			}
		}
		comp.Props.Add(p)
	}
}

func emitAttachment(props ical.Props, att model.Attachment) {
	p := &ical.Prop{Name: ical.PropAttach, Params: make(ical.Params)}
	if att.Data != nil {
		// Inline binary attachment
		p.Value = base64.StdEncoding.EncodeToString(att.Data)
		p.Params.Set("ENCODING", "BASE64")
		p.Params.Set("VALUE", "BINARY")
		if att.Filename != "" {
			p.Params.Set("FILENAME", att.Filename)
		}
	} else {
		p.Value = att.URI
	}
	if att.FmtType != "" {
		p.Params.Set("FMTTYPE", att.FmtType)
	}
	props.Add(p)
}

func ExportJournals(journals []journal.Journal, calName string) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProductID)
	cal.Props.SetText("CALSCALE", "GREGORIAN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	// Emit VTIMEZONE components for all referenced timezones.
	seen := make(map[string]bool)
	for _, j := range journals {
		if j.Timezone != "" && j.Timezone != "FLOATING" && !seen[j.Timezone] {
			seen[j.Timezone] = true
			if vtz, err := buildVTimezone(j.Timezone); err == nil {
				cal.Children = append(cal.Children, vtz)
			}
		}
	}

	for _, j := range journals {
		vjournal := ical.NewComponent(ical.CompJournal)

		vjournal.Props.SetText(ical.PropUID, j.UID)
		vjournal.Props.SetText(ical.PropSummary, j.Summary)

		if j.Description != "" {
			vjournal.Props.SetText(ical.PropDescription, j.Description)
		}

		// DTSTART with timezone handling
		if j.StartDate != "" {
			if d, err := time.Parse("2006-01-02", j.StartDate); err == nil {
				vjournal.Props.SetDate(ical.PropDateTimeStart, d)
			} else if start, err := time.Parse(time.RFC3339, j.StartDate); err == nil {
				if j.Timezone == "FLOATING" {
					p := &ical.Prop{Name: ical.PropDateTimeStart}
					p.Value = start.UTC().Format("20060102T150405")
					vjournal.Props.Set(p)
				} else if j.Timezone != "" {
					if loc, lerr := time.LoadLocation(j.Timezone); lerr == nil {
						vjournal.Props.SetDateTime(ical.PropDateTimeStart, start.In(loc))
						if p := vjournal.Props.Get(ical.PropDateTimeStart); p != nil {
							p.Params.Set(ical.ParamTimezoneID, j.Timezone)
						}
					} else {
						vjournal.Props.SetDateTime(ical.PropDateTimeStart, start.UTC())
					}
				} else {
					vjournal.Props.SetDateTime(ical.PropDateTimeStart, start.UTC())
				}
			}
		}

		vjournal.Props.SetText(ical.PropStatus, j.Status)

		seq := &ical.Prop{Name: "SEQUENCE"}
		seq.Value = strconv.FormatInt(j.Sequence, 10)
		vjournal.Props.Set(seq)

		if j.Class != "" && j.Class != "PUBLIC" {
			vjournal.Props.SetText(ical.PropClass, j.Class)
		}

		if j.URL != "" {
			p := &ical.Prop{Name: ical.PropURL}
			p.Value = j.URL
			vjournal.Props.Set(p)
		}

		if j.Categories != "" {
			catProp := &ical.Prop{Name: ical.PropCategories}
			catProp.SetTextList(j.ParseCategories())
			vjournal.Props.Set(catProp)
		}

		if j.RecurrenceRule != "" {
			rruleProp := &ical.Prop{Name: ical.PropRecurrenceRule}
			rruleProp.Value = j.RecurrenceRule
			vjournal.Props.Set(rruleProp)
		}

		// Dates
		emitDateListOnComponent(vjournal, ical.PropExceptionDates, j.ExDates)
		emitDateListOnComponent(vjournal, ical.PropRecurrenceDates, j.RDates)

		if j.RecurrenceID != "" {
			// A VJOURNAL is all-day when its DTSTART is a date-only value;
			// the RECURRENCE-ID type must match.
			emitRecurrenceID(vjournal.Props, j.RecurrenceID, timeutil.IsDateOnly(j.StartDate))
		}

		if j.DtStamp != "" {
			if ts, err := time.Parse(time.RFC3339, j.DtStamp); err == nil {
				vjournal.Props.SetDateTime(ical.PropDateTimeStamp, ts.UTC())
			} else {
				vjournal.Props.SetDateTime(ical.PropDateTimeStamp, j.UpdatedAt.UTC())
			}
		} else {
			vjournal.Props.SetDateTime(ical.PropDateTimeStamp, j.UpdatedAt.UTC())
		}
		vjournal.Props.SetDateTime(ical.PropCreated, j.CreatedAt.UTC())
		vjournal.Props.SetDateTime(ical.PropLastModified, j.UpdatedAt.UTC())

		// ATTACH
		for _, att := range j.Attachments {
			emitAttachment(vjournal.Props, att)
		}

		// COMMENT
		for _, c := range j.Comments {
			p := &ical.Prop{Name: ical.PropComment}
			p.SetText(c)
			vjournal.Props.Add(p)
		}

		// CONTACT
		for _, c := range j.Contacts {
			p := &ical.Prop{Name: ical.PropContact}
			p.SetText(c)
			vjournal.Props.Add(p)
		}

		// RELATED-TO
		for _, r := range j.Relations {
			p := &ical.Prop{Name: ical.PropRelatedTo, Params: make(ical.Params)}
			p.Value = r.RelUID
			if r.RelType != "" && r.RelType != "PARENT" {
				p.Params.Set("RELTYPE", r.RelType)
			}
			vjournal.Props.Add(p)
		}

		// X-Properties (round-trip preservation)
		emitXProperties(vjournal, j.XProperties)

		// ATTENDEE / ORGANIZER
		for _, att := range j.Attendees {
			if att.Organizer {
				org := &ical.Prop{Name: ical.PropOrganizer, Params: make(ical.Params)}
				org.Value = "mailto:" + att.Email
				if att.Name != "" {
					org.Params.Set(ical.ParamCommonName, att.Name)
				}
				setOrganizerParams(org, att)
				vjournal.Props.Set(org)
			}
			attendee := &ical.Prop{Name: ical.PropAttendee, Params: make(ical.Params)}
			attendee.Value = "mailto:" + att.Email
			if att.Name != "" {
				attendee.Params.Set(ical.ParamCommonName, att.Name)
			}
			attendee.Params.Set(ical.ParamParticipationStatus, att.RSVPStatus)
			attendee.Params.Set(ical.ParamRole, att.Role)
			setAttendeeParams(attendee, att)
			vjournal.Props.Add(attendee)
		}

		cal.Children = append(cal.Children, vjournal)
	}

	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return nil, fmt.Errorf("encode ical: %w", err)
	}
	return buf.Bytes(), nil
}
