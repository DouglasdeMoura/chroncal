package ical

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/journal"
)

// journalYear returns the calendar year to anchor a journal's VTIMEZONE on.
// It prefers its start date and falls back to the current year.
func journalYear(j journal.Journal) int {
	if d := j.ParseStartDate(); !d.IsZero() {
		return d.Year()
	}
	return time.Now().Year()
}

func ExportJournals(journals []journal.Journal, calName string) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProductID)
	cal.Props.SetText("CALSCALE", "GREGORIAN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	// Emit VTIMEZONE components for all referenced timezones, anchored on the
	// years the journals actually fall in (issue #515), widened across a
	// recurring journal's horizon to cover every DST-rule era it crosses
	// (issue #518).
	var spans tzSpans
	for _, j := range journals {
		spans.add(j.Timezone, journalYear(j))
		if j.RecurrenceRule != "" {
			if a := j.ParseStartDate(); !a.IsZero() {
				spans.add(j.Timezone, recurrenceEndYear(j.RecurrenceRule, a))
			}
		}
	}
	spans.emit(cal)

	masters := seriesRIDTypes(journals,
		func(j journal.Journal) string { return j.UID },
		func(j journal.Journal) string { return j.RecurrenceID },
		func(j journal.Journal) string { return j.StartDate },
		func(j journal.Journal) string { return j.Timezone })
	for _, j := range journals {
		vjournal := ical.NewComponent(ical.CompJournal)

		vjournal.Props.SetText(ical.PropUID, j.UID)
		vjournal.Props.SetText(ical.PropSummary, j.Summary)

		// DESCRIPTION. RFC 5545 permits multiple; emit one per element when the
		// per-property split survived (a fresh import), otherwise fall back to
		// the single DB-backed Description value.
		if len(j.Descriptions) > 0 {
			for _, d := range j.Descriptions {
				p := &ical.Prop{Name: ical.PropDescription}
				p.SetText(d)
				vjournal.Props.Add(p)
			}
		} else if j.Description != "" {
			vjournal.Props.SetText(ical.PropDescription, j.Description)
		}

		// DTSTART with timezone handling
		if j.StartDate != "" {
			if d, err := time.Parse("2006-01-02", j.StartDate); err == nil {
				vjournal.Props.SetDate(ical.PropDateTimeStart, d)
			} else if start, err := time.Parse(time.RFC3339, j.StartDate); err == nil {
				setZonedDateTime(vjournal.Props, ical.PropDateTimeStart, start, j.Timezone)
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
		emitDateListOnComponent(vjournal, ical.PropExceptionDates, j.ExDates, j.Timezone)
		emitDateListOnComponent(vjournal, ical.PropRecurrenceDates, j.RDates, j.Timezone)

		if j.RecurrenceID != "" {
			// The RECURRENCE-ID takes the type of the DTSTART of the series.
			rt := overrideRIDType(masters, j.UID, j.StartDate, j.Timezone)
			emitRecurrenceID(vjournal.Props, j.RecurrenceID, rt.allDay, rt.floating)
		}

		emitTimestamps(vjournal.Props, j.DtStamp, j.CreatedAt, j.UpdatedAt)

		// ATTACH
		for _, att := range j.Attachments {
			emitAttachment(vjournal.Props, att)
		}

		// COMMENT
		emitTextProps(vjournal.Props, ical.PropComment, j.Comments)

		// CONTACT
		emitTextProps(vjournal.Props, ical.PropContact, j.Contacts)

		// RELATED-TO
		emitRelations(vjournal.Props, j.Relations)

		// X-Properties (round-trip preservation)
		emitXProperties(vjournal, j.XProperties)

		emitAttendees(vjournal.Props, j.Attendees)

		cal.Children = append(cal.Children, vjournal)
	}

	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return nil, fmt.Errorf("encode ical: %w", err)
	}
	return buf.Bytes(), nil
}
