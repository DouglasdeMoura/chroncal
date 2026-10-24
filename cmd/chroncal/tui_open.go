package main

import (
	"context"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

func resolveTUIOpenEvent(ctx context.Context, a *app.App, ref, recurrenceID, at string) (event.Event, error) {
	if ref == "" {
		return event.Event{}, errInvalidInputf("--event is required to open an event in the TUI")
	}
	if at != "" && recurrenceID != "" {
		return event.Event{}, errInvalidInputf("--at cannot be combined with --recurrence-id")
	}
	ev, err := resolveEvent(ctx, a, ref, recurrenceID)
	if err != nil {
		return event.Event{}, err
	}
	if at == "" {
		return ev, nil
	}
	when, dateOnly, err := parseTUIAt(at, time.Now())
	if err != nil {
		return event.Event{}, err
	}
	if occ, ok, err := findSeriesOccurrence(ctx, a, ev, when, dateOnly); err != nil {
		return event.Event{}, err
	} else if ok {
		return occ, nil
	}
	span := ev.EndTime.Sub(ev.StartTime)
	ev.StartTime = when
	if span > 0 {
		ev.EndTime = when.Add(span)
	}
	return ev, nil
}

// findSeriesOccurrence finds the occurrence of the series of master at the
// --at value (issue #878). A date-only value takes the first occurrence on
// that local date. A timestamp takes the occurrence that starts at that
// time, or the occurrence that an override moved from that time. An
// override gives its own row, so the TUI shows its own fields. ok is false
// when master is not a recurring master or when no occurrence matches.
func findSeriesOccurrence(ctx context.Context, a *app.App, master event.Event, when time.Time, dateOnly bool) (event.Event, bool, error) {
	if master.RecurrenceID != "" || (master.RecurrenceRule == "" && master.RDates == "") {
		return event.Event{}, false, nil
	}
	// The window adds one day on each side. An all-day occurrence is a UTC
	// midnight, and a moved override can start on the day before or after.
	expanded, err := a.Recurrences.ListExpandedEvents(ctx, when.AddDate(0, 0, -1), when.AddDate(0, 0, 2))
	if err != nil {
		return event.Event{}, false, err
	}
	dayKey := when.Local().Format(time.DateOnly)
	for _, x := range expanded {
		if x.UID != master.UID || x.CalendarID != master.CalendarID {
			continue
		}
		occ := x.Event
		// slot is the time of the occurrence in the series. A generated
		// occurrence keeps the times of the master, so it takes the slot.
		slot := x.InstanceTime
		if occ.RecurrenceID == "" {
			occ.EndTime = slot.Add(occ.EndTime.Sub(occ.StartTime))
			occ.StartTime = slot
		} else if rid, err := timeutil.ParseRecurrenceID(occ.RecurrenceID); err == nil {
			slot = rid
		}
		if dateOnly {
			if occurrenceDate(occ) == dayKey {
				return occ, true, nil
			}
			continue
		}
		if occ.StartTime.Equal(when) || slot.Equal(when) {
			return occ, true, nil
		}
	}
	return event.Event{}, false, nil
}

// occurrenceDate returns the calendar date that the TUI shows ev on. An
// all-day event stores a UTC midnight. A timed event uses the local date.
func occurrenceDate(ev event.Event) string {
	if ev.AllDay {
		return ev.StartTime.UTC().Format(time.DateOnly)
	}
	return ev.StartTime.Local().Format(time.DateOnly)
}

// parseTUIAt parses the --at flag. It accepts an RFC 3339 timestamp, a
// YYYY-MM-DD date, or a relative date word resolved against now in the
// local timezone (issue #785). Date-only values resolve to the start of
// that day (see startOfDay), and dateOnly is then true.
func parseTUIAt(value string, now time.Time) (when time.Time, dateOnly bool, err error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, false, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, false, nil
	}
	if t, ok := resolveRelativeDate(value, now, time.Local); ok {
		return t, true, nil
	}
	d, ok := parseCivilDate(value)
	if !ok {
		return time.Time{}, false, errInvalidInputf("--at: invalid time %q (expected RFC 3339, YYYY-MM-DD, or a relative date like tomorrow)", value)
	}
	return startOfDay(d, time.Local), true, nil
}
