package main

import (
	"context"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
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
	when, err := parseTUIAt(at, time.Now())
	if err != nil {
		return event.Event{}, err
	}
	span := ev.EndTime.Sub(ev.StartTime)
	ev.StartTime = when
	if span > 0 {
		ev.EndTime = when.Add(span)
	}
	return ev, nil
}

// parseTUIAt parses the --at flag. It accepts an RFC 3339 timestamp, a
// YYYY-MM-DD date, or a relative date word resolved against now in the
// local timezone (issue #785). Date-only values resolve to the start of
// that day (see startOfDay).
func parseTUIAt(value string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	if t, ok := resolveRelativeDate(value, now, time.Local); ok {
		return t, nil
	}
	d, ok := parseCivilDate(value)
	if !ok {
		return time.Time{}, errInvalidInputf("--at: invalid time %q (expected RFC 3339, YYYY-MM-DD, or a relative date like tomorrow)", value)
	}
	return startOfDay(d, time.Local), nil
}
