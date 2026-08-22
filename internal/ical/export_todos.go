package ical

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func ExportTodos(todos []todo.Todo, calName string) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProductID)
	cal.Props.SetText("CALSCALE", "GREGORIAN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	// Emit VTIMEZONE components for all referenced timezones, anchored on the
	// years the todos actually fall in (issue #515), widened across a recurring
	// todo's horizon so every DST-rule era it crosses is covered (issue #518).
	var spans tzSpans
	for _, t := range todos {
		spans.add(t.Timezone, todoYear(t))
		if t.RecurrenceRule != "" {
			if a := todoAnchor(t); !a.IsZero() {
				spans.add(t.Timezone, recurrenceEndYear(t.RecurrenceRule, a))
			}
		}
	}
	spans.emit(cal)

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
		// The UsableSpan guard covers DB rows written before import
		// validated the todo DURATION; a stored bad or negative value
		// must not reach the server.
		if duration.UsableSpan(t.Duration) &&
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
		emitDateListOnComponent(vtodo, ical.PropExceptionDates, t.ExDates, t.Timezone)
		emitDateListOnComponent(vtodo, ical.PropRecurrenceDates, t.RDates, t.Timezone)

		if t.RecurrenceID != "" {
			// A VTODO is all-day when its recurrence anchor (DTSTART, else DUE)
			// is a date-only value; the RECURRENCE-ID type must match.
			anchor := t.StartDate
			if anchor == "" {
				anchor = t.DueDate
			}
			emitRecurrenceID(vtodo.Props, t.RecurrenceID, timeutil.IsDateOnly(anchor), t.Timezone == "FLOATING")
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
			if v := buildValarm(alarm, t.Timezone); v != nil {
				vtodo.Children = append(vtodo.Children, v)
			}
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

// todoAnchor returns a todo's recurrence/VTIMEZONE anchor date. It prefers its
// start date over its due date. Returns the zero time when the todo carries
// neither.
func todoAnchor(t todo.Todo) time.Time {
	if d := t.ParseStartDate(); !d.IsZero() {
		return d
	}
	return t.ParseDueDate()
}

// todoYear returns the calendar year to anchor a todo's VTIMEZONE on. It
// prefers its start date, then its due date. It falls back to the current
// year when the todo carries neither.
func todoYear(t todo.Todo) int {
	if a := todoAnchor(t); !a.IsZero() {
		return a.Year()
	}
	return time.Now().Year()
}
