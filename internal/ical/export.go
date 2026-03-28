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
			vevent.Props.SetText(ical.PropCategories, e.Categories)
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

		// RESOURCES
		if len(e.Resources) > 0 {
			vevent.Props.SetText(ical.PropResources, strings.Join(e.Resources, ","))
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
		vevent.Props.SetDate(ical.PropDateTimeStart, e.StartTime.UTC())
		vevent.Props.SetDate(ical.PropDateTimeEnd, e.EndTime.UTC())
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

		// DUE or DTSTART+DURATION
		if t.DueDate != "" {
			if due, err := time.Parse(time.RFC3339, t.DueDate); err == nil {
				vtodo.Props.SetDateTime(ical.PropDue, due.UTC())
			}
		}
		if t.StartDate != "" {
			if start, err := time.Parse(time.RFC3339, t.StartDate); err == nil {
				vtodo.Props.SetDateTime(ical.PropDateTimeStart, start.UTC())
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
			vtodo.Props.SetText(ical.PropCategories, t.Categories)
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

		// RESOURCES
		if len(t.Resources) > 0 {
			vtodo.Props.SetText(ical.PropResources, strings.Join(t.Resources, ","))
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

	return []byte(aStr + bStr + "END:VCALENDAR\r\n")
}

func emitDateListOnComponent(comp *ical.Component, propName, dates string) {
	if dates == "" {
		return
	}
	for _, ds := range strings.Split(dates, ",") {
		ds = strings.TrimSpace(ds)
		if t, err := time.Parse(time.RFC3339, ds); err == nil {
			prop := &ical.Prop{Name: propName, Params: make(ical.Params)}
			prop.SetDateTime(t.UTC())
			comp.Props.Add(prop)
		}
	}
}

func buildValarm(alarm model.Alarm) *ical.Component {
	valarm := ical.NewComponent(ical.CompAlarm)
	valarm.Props.SetText(ical.PropAction, alarm.Action)

	trigger := &ical.Prop{Name: ical.PropTrigger, Params: make(ical.Params)}
	trigger.Value = alarm.TriggerValue
	if strings.HasPrefix(alarm.TriggerValue, "-") || strings.HasPrefix(alarm.TriggerValue, "P") {
		trigger.Params.Set("VALUE", "DURATION")
	}
	if alarm.Related == "END" {
		trigger.Params.Set("RELATED", "END")
	}
	valarm.Props.Set(trigger)

	if alarm.Description != "" {
		valarm.Props.SetText(ical.PropDescription, alarm.Description)
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
