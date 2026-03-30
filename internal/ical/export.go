package ical

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/todo"
)

func ExportEvents(events []event.Event, calName string) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//tcal//tcal//EN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	// Emit VTIMEZONE components for all referenced timezones (RFC 5545 Section 3.6.5).
	seen := make(map[string]bool)
	for _, e := range events {
		if e.Timezone != "" && !seen[e.Timezone] {
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

		if e.Categories != "" {
			// CATEGORIES is a comma-separated list of TEXT values.
			// SetTextList handles escaping within individual values and
			// uses unescaped commas as separators per RFC 5545 Section 3.8.1.2.
			catProp := &ical.Prop{Name: ical.PropCategories}
			catProp.SetTextList(strings.Split(e.Categories, ","))
			vevent.Props.Set(catProp)
		}

		// EXDATE
		emitDateList(vevent, ical.PropExceptionDates, e.ExDates)

		// RDATE
		emitDateList(vevent, ical.PropRecurrenceDates, e.RDates)

		// RECURRENCE-ID
		if e.RecurrenceID != "" {
			if t, err := time.Parse(time.RFC3339, e.RecurrenceID); err == nil {
				vevent.Props.SetDateTime(ical.PropRecurrenceID, t.UTC())
			}
		}

		if e.Geo != "" {
			p := &ical.Prop{Name: ical.PropGeo}
			p.Value = e.Geo
			vevent.Props.Set(p)
		}

		vevent.Props.SetDateTime(ical.PropDateTimeStamp, e.UpdatedAt.UTC())
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
				vevent.Props.Set(org)
			}

			attendee := &ical.Prop{Name: ical.PropAttendee, Params: make(ical.Params)}
			attendee.Value = "mailto:" + att.Email
			if att.Name != "" {
				attendee.Params.Set(ical.ParamCommonName, att.Name)
			}
			attendee.Params.Set(ical.ParamParticipationStatus, att.RSVPStatus)
			attendee.Params.Set(ical.ParamRole, att.Role)
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

func setEventTimes(vevent *ical.Event, e event.Event) {
	if e.AllDay {
		// Use the time as-is (not UTC) so SetDate extracts the correct
		// local date. Converting to UTC first shifts the date for
		// timezones with positive UTC offsets (e.g. UTC+12).
		vevent.Props.SetDate(ical.PropDateTimeStart, e.StartTime)
		vevent.Props.SetDate(ical.PropDateTimeEnd, e.EndTime)
	} else if e.Timezone != "" {
		loc, err := time.LoadLocation(e.Timezone)
		if err == nil {
			vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.In(loc))
			if prop := vevent.Props.Get(ical.PropDateTimeStart); prop != nil {
				prop.Params.Set(ical.ParamTimezoneID, e.Timezone)
			}
			vevent.Props.SetDateTime(ical.PropDateTimeEnd, e.EndTime.In(loc))
			if prop := vevent.Props.Get(ical.PropDateTimeEnd); prop != nil {
				prop.Params.Set(ical.ParamTimezoneID, e.Timezone)
			}
		} else {
			// Fallback to UTC
			vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.UTC())
			vevent.Props.SetDateTime(ical.PropDateTimeEnd, e.EndTime.UTC())
		}
	} else {
		vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.UTC())
		vevent.Props.SetDateTime(ical.PropDateTimeEnd, e.EndTime.UTC())
	}
}

func emitDateList(vevent *ical.Event, propName, dates string) {
	if dates == "" {
		return
	}
	for _, ds := range strings.Split(dates, ",") {
		ds = strings.TrimSpace(ds)
		if t, err := time.Parse(time.RFC3339, ds); err == nil {
			prop := &ical.Prop{Name: propName, Params: make(ical.Params)}
			prop.SetDateTime(t.UTC())
			vevent.Props.Add(prop)
		}
	}
}

func ExportTodos(todos []todo.Todo, calName string) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//tcal//tcal//EN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	// Emit VTIMEZONE components for all referenced timezones.
	seen := make(map[string]bool)
	for _, t := range todos {
		if t.Timezone != "" && !seen[t.Timezone] {
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
				if t.Timezone != "" {
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
				if t.Timezone != "" {
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
		if t.Duration != "" {
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
			catProp.SetTextList(strings.Split(t.Categories, ","))
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
			if rid, err := time.Parse(time.RFC3339, t.RecurrenceID); err == nil {
				vtodo.Props.SetDateTime(ical.PropRecurrenceID, rid.UTC())
			}
		}

		if t.Geo != "" {
			p := &ical.Prop{Name: ical.PropGeo}
			p.Value = t.Geo
			vtodo.Props.Set(p)
		}

		vtodo.Props.SetDateTime(ical.PropDateTimeStamp, t.UpdatedAt.UTC())
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
				vtodo.Props.Set(org)
			}
			attendee := &ical.Prop{Name: ical.PropAttendee, Params: make(ical.Params)}
			attendee.Value = "mailto:" + att.Email
			if att.Name != "" {
				attendee.Params.Set(ical.ParamCommonName, att.Name)
			}
			attendee.Params.Set(ical.ParamParticipationStatus, att.RSVPStatus)
			attendee.Params.Set(ical.ParamRole, att.Role)
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

	// Remove header from b (everything up to and including first blank line or first BEGIN:V component)
	if idx := strings.Index(bStr, "BEGIN:VTODO"); idx >= 0 {
		bStr = bStr[idx:]
	} else if idx := strings.Index(bStr, "BEGIN:VEVENT"); idx >= 0 {
		bStr = bStr[idx:]
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
	if alarm.Duration != "" {
		p := &ical.Prop{Name: ical.PropDuration}
		p.Value = alarm.Duration
		valarm.Props.Set(p)
	}
	if alarm.Repeat > 0 {
		p := &ical.Prop{Name: "REPEAT"}
		p.Value = strconv.Itoa(alarm.Repeat)
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

	addSubComp := func(compName, tzName string, offset, fromOffset int, dtstart time.Time) {
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

		vtz.Children = append(vtz.Children, comp)
	}

	if len(transitions) == 0 {
		// No DST — single STANDARD component
		addSubComp("STANDARD", janName, janOffset, janOffset,
			time.Date(refYear, 1, 1, 0, 0, 0, 0, loc))
	} else {
		for _, tr := range transitions {
			// The transition was detected between month M-1 and M, but the
			// exact day could be anywhere in the prior month. Search backward.
			dtstart := findTransitionDay(loc, refYear, tr.month, tr.fromOffset)
			compName := "STANDARD"
			if tr.offset > tr.fromOffset {
				compName = "DAYLIGHT"
			}
			addSubComp(compName, tr.name, tr.offset, tr.fromOffset, dtstart)
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
