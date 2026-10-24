package main

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/tui"
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
	} else if dateOnly && isSeriesMaster(ev) {
		// The series has no occurrence on that date. Do not show a made-up
		// occurrence at 00:00 (issue #878).
		return event.Event{}, errInvalidInputf("--at: the series has no occurrence on %s", when.Format(time.DateOnly))
	}
	span := ev.EndTime.Sub(ev.StartTime)
	ev.StartTime = when
	if span > 0 {
		ev.EndTime = when.Add(span)
	}
	return ev, nil
}

// findSeriesOccurrence finds the occurrence of the series of master at the
// --at value (issue #878). ok is false when master is not a recurring
// master or when no occurrence matches.
//
//   - A timestamp takes the override whose RECURRENCE-ID is that time, then
//     the occurrence that starts at that time.
//   - A date takes the first occurrence that starts on that local date,
//     then the first occurrence that the TUI grid shows on that date.
//
// An override gives its own row, so the TUI shows its own fields.
func findSeriesOccurrence(ctx context.Context, a *app.App, master event.Event, when time.Time, dateOnly bool) (event.Event, bool, error) {
	if !isSeriesMaster(master) {
		return event.Event{}, false, nil
	}
	if !dateOnly {
		_, override, err := a.Events.SeriesOccurrence(ctx, master.CalendarID, master.UID, when.UTC().Format(time.RFC3339))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return event.Event{}, false, err
		}
		// The grid does not show a cancelled override.
		if override != nil && !strings.EqualFold(override.Status, "CANCELLED") {
			return *override, true, nil
		}
	}
	// The window adds one day on each side. An all-day occurrence is a UTC
	// midnight, and an occurrence can start on the day before.
	expanded, err := a.Recurrences.ListExpandedEvents(ctx, when.AddDate(0, 0, -1), when.AddDate(0, 0, 2), recurrence.SkipCategories())
	if err != nil {
		return event.Event{}, false, err
	}
	dayKey := when.Local().Format(time.DateOnly)
	var shown event.Event
	for _, x := range expanded {
		if x.UID != master.UID {
			continue
		}
		occ := x.Event
		if occ.RecurrenceID == "" {
			if occ.CalendarID != master.CalendarID {
				continue
			}
			// A generated occurrence keeps the times of the master.
			occ.EndTime = x.InstanceTime.Add(occ.EndTime.Sub(occ.StartTime))
			occ.StartTime = x.InstanceTime
		} else if occ.CalendarID != master.CalendarID {
			// A legacy override can sit in another calendar (issue #842).
			owned, err := a.Events.IsSeriesRowOf(ctx, master.CalendarID, occ.ID)
			if err != nil {
				return event.Event{}, false, err
			}
			if !owned {
				continue
			}
		}
		if !dateOnly {
			if occ.StartTime.Equal(when) {
				return occ, true, nil
			}
			continue
		}
		days := tui.EventCalendarDays(occ)
		if days[0].Format(time.DateOnly) == dayKey {
			return occ, true, nil
		}
		if shown.ID == 0 && slices.ContainsFunc(days, func(d time.Time) bool { return d.Format(time.DateOnly) == dayKey }) {
			shown = occ
		}
	}
	return shown, shown.ID != 0, nil
}

// isSeriesMaster reports whether ev is the master of a recurring series.
func isSeriesMaster(ev event.Event) bool {
	return ev.RecurrenceID == "" && (ev.RecurrenceRule != "" || ev.RDates != "")
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
