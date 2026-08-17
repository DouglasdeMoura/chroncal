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
	when, err := parseTUIAt(at)
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

func parseTUIAt(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, errInvalidInputf("--at: invalid time %q (expected RFC 3339 or YYYY-MM-DD)", value)
	}
	return t, nil
}
