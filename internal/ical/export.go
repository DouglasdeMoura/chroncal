package ical

import (
	"bytes"
	"fmt"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/tcal/internal/event"
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

		if e.AllDay {
			vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.UTC())
			// For all-day events, use VALUE=DATE
			if prop := vevent.Props.Get(ical.PropDateTimeStart); prop != nil {
				prop.Params.Set("VALUE", "DATE")
			}
			vevent.Props.SetDateTime(ical.PropDateTimeEnd, e.EndTime.UTC())
			if prop := vevent.Props.Get(ical.PropDateTimeEnd); prop != nil {
				prop.Params.Set("VALUE", "DATE")
			}
		} else {
			vevent.Props.SetDateTime(ical.PropDateTimeStart, e.StartTime.UTC())
			vevent.Props.SetDateTime(ical.PropDateTimeEnd, e.EndTime.UTC())
		}

		if e.RecurrenceRule != "" {
			vevent.Props.SetText(ical.PropRecurrenceRule, e.RecurrenceRule)
		}

		vevent.Props.SetDateTime(ical.PropDateTimeStamp, e.UpdatedAt.UTC())
		vevent.Props.SetDateTime(ical.PropCreated, e.CreatedAt.UTC())
		vevent.Props.SetDateTime(ical.PropLastModified, e.UpdatedAt.UTC())

		cal.Children = append(cal.Children, vevent.Component)
	}

	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return nil, fmt.Errorf("encode ical: %w", err)
	}
	return buf.Bytes(), nil
}
