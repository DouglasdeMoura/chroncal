package ical

import (
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/tcal/internal/event"
)

func ImportFile(r io.Reader) ([]event.Event, error) {
	dec := ical.NewDecoder(r)
	var events []event.Event

	for {
		cal, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode ical: %w", err)
		}

		for _, child := range cal.Children {
			if child.Name != ical.CompEvent {
				continue
			}
			vevent := ical.Event{Component: child}
			e, err := eventFromVEvent(vevent)
			if err != nil {
				continue
			}
			events = append(events, e)
		}
	}

	return events, nil
}

func eventFromVEvent(ve ical.Event) (event.Event, error) {
	uid, err := ve.Props.Text(ical.PropUID)
	if err != nil || uid == "" {
		return event.Event{}, fmt.Errorf("missing UID")
	}

	summary, _ := ve.Props.Text(ical.PropSummary)
	description, _ := ve.Props.Text(ical.PropDescription)
	location, _ := ve.Props.Text(ical.PropLocation)

	startTime, err := ve.Props.DateTime(ical.PropDateTimeStart, nil)
	if err != nil {
		return event.Event{}, fmt.Errorf("parse DTSTART: %w", err)
	}

	endTime, err := ve.Props.DateTime(ical.PropDateTimeEnd, nil)
	if err != nil {
		// If no DTEND, use DTSTART + 1 hour as default
		endTime = startTime.Add(1 * 60 * 60 * 1e9)
	}

	allDay := false
	if prop := ve.Props.Get(ical.PropDateTimeStart); prop != nil {
		if strings.EqualFold(prop.Params.Get("VALUE"), "DATE") {
			allDay = true
		}
	}

	rrule, _ := ve.Props.Text(ical.PropRecurrenceRule)

	return event.Event{
		UID:            uid,
		Title:          summary,
		Description:    description,
		Location:       location,
		StartTime:      startTime.UTC(),
		EndTime:        endTime.UTC(),
		AllDay:         allDay,
		RecurrenceRule: rrule,
	}, nil
}
