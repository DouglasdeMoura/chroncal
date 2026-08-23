package ical

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/event"
)

func ExportEvents(events []event.Event, calName string) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProductID)
	cal.Props.SetText("CALSCALE", "GREGORIAN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	// Emit VTIMEZONE components for all referenced timezones (RFC 5545 Section
	// 3.6.5), anchored on the years the events actually fall in (issue #515).
	// A recurring series widens the span to its last-occurrence year so the
	// VTIMEZONE covers every DST-rule era the series crosses (issue #518).
	var spans tzSpans
	for _, e := range events {
		spans.add(e.Timezone, e.StartTime.Year())
		if e.RecurrenceRule != "" {
			spans.add(e.Timezone, recurrenceEndYear(e.RecurrenceRule, e.StartTime))
		}
	}
	spans.emit(cal)

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
		emitDateListOnComponent(vevent.Component, ical.PropExceptionDates, e.ExDates, e.Timezone)

		// RDATE
		emitDateListOnComponent(vevent.Component, ical.PropRecurrenceDates, e.RDates, e.Timezone)

		// RECURRENCE-ID
		if e.RecurrenceID != "" {
			emitRecurrenceID(vevent.Props, e.RecurrenceID, e.AllDay, e.Timezone == "FLOATING")
		}

		if e.Geo != "" {
			p := &ical.Prop{Name: ical.PropGeo}
			p.Value = e.Geo
			vevent.Props.Set(p)
		}

		emitTimestamps(vevent.Props, e.DtStamp, e.CreatedAt, e.UpdatedAt)

		// ATTACH
		for _, att := range e.Attachments {
			emitAttachment(vevent.Props, att)
		}

		emitTextProps(vevent.Props, ical.PropComment, e.Comments)

		emitTextProps(vevent.Props, ical.PropContact, e.Contacts)

		// RESOURCES (comma-separated list, like CATEGORIES)
		emitResources(vevent.Props, e.Resources)

		// RELATED-TO
		emitRelations(vevent.Props, e.Relations)

		// X-Properties (round-trip preservation)
		emitXProperties(vevent.Component, e.XProperties)

		// VALARM children
		for _, alarm := range e.Alarms {
			if alarm.Summary == "" && alarm.Action == "EMAIL" {
				alarm.Summary = e.Title
			}
			if v := buildValarm(alarm, e.Timezone); v != nil {
				vevent.Children = append(vevent.Children, v)
			}
		}

		emitAttendees(vevent.Props, e.Attendees)

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
	// The UsableSpan guard exists for DB rows written before import
	// validated DURATION (the same reason as the alarm-path guard in
	// buildValarm). A stored bad or negative value must not reach the
	// server; the stored end time takes over as DTEND.
	useDuration := duration.UsableSpan(e.DurationValue)

	// A legacy row whose span failed the guard above also holds an end
	// time that the same bad span produced, so the end can precede the
	// start. An inverted interval is invalid iCal. That is the defect
	// the guard prevents, in another shape. Fall back to the span the
	// importer gives a broken value. An end equal to the start stays:
	// RFC 5545 allows a zero-length event.
	endTime := e.EndTime
	if !useDuration && endTime.Before(e.StartTime) {
		if e.AllDay {
			endTime = e.StartTime.AddDate(0, 0, 1)
		} else {
			endTime = e.StartTime.Add(time.Hour)
		}
	}

	if e.AllDay {
		vevent.Props.SetDate(ical.PropDateTimeStart, allDayExportDate(e.StartTime, e.Timezone))
		if useDuration {
			setPropDuration(vevent, e.DurationValue)
		} else {
			vevent.Props.SetDate(ical.PropDateTimeEnd, allDayExportDate(endTime, e.Timezone))
		}
	} else if e.Timezone == "FLOATING" {
		// Floating times are host-independent wall clocks. Import interprets
		// them as UTC, so export must emit the stored UTC wall clock (not
		// .Local(), which would shift the clock on non-UTC hosts).
		setPropFloating(vevent, ical.PropDateTimeStart, e.StartTime.UTC())
		if useDuration {
			setPropDuration(vevent, e.DurationValue)
		} else {
			setPropFloating(vevent, ical.PropDateTimeEnd, endTime.UTC())
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
				vevent.Props.SetDateTime(ical.PropDateTimeEnd, endTime.In(loc))
				if prop := vevent.Props.Get(ical.PropDateTimeEnd); prop != nil {
					prop.Params.Set(ical.ParamTimezoneID, e.Timezone)
				}
			}
		} else {
			vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.UTC())
			if useDuration {
				setPropDuration(vevent, e.DurationValue)
			} else {
				vevent.Props.SetDateTime(ical.PropDateTimeEnd, endTime.UTC())
			}
		}
	} else {
		vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.UTC())
		if useDuration {
			setPropDuration(vevent, e.DurationValue)
		} else {
			vevent.Props.SetDateTime(ical.PropDateTimeEnd, endTime.UTC())
		}
	}

	// A CalDAV import of a DTEND that failed to parse stores the raw
	// server string in X-CHRONCAL-ORIGINAL-DTEND and fabricates a local
	// span. Emit the stored string as DTEND: the server receives the exact
	// value it sent, and the fabricated span does not overwrite it (issue
	// #567). A local edit that changes the span clears the slot first (see
	// internal/event, issue #649). This override then applies only while
	// the local span still matches the server value.
	if !useDuration {
		for _, xp := range e.XProperties {
			if xp.Name != xpropOriginalDTEND {
				continue
			}
			p := propFromXProperty(xp)
			p.Name = ical.PropDateTimeEnd
			vevent.Props.Set(p)
			break
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
