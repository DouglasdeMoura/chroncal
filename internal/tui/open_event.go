package tui

import (
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// applyOccurrenceTimes copies the requested instance start/end onto a
// generated recurring master. One-offs and stored overrides keep the
// stored times. The event view then shows the occurrence the user opened,
// not the series DTSTART.
func applyOccurrenceTimes(stored, requested event.Event) event.Event {
	generated := (stored.RecurrenceRule != "" || stored.RDates != "") && stored.RecurrenceID == ""
	if !generated || requested.StartTime.IsZero() {
		return stored
	}
	span := stored.EndTime.Sub(stored.StartTime)
	stored.StartTime = requested.StartTime
	if !requested.EndTime.IsZero() {
		stored.EndTime = requested.EndTime
	} else if span > 0 {
		stored.EndTime = requested.StartTime.Add(span)
	}
	return stored
}

// matchOpenEvent finds the loaded occurrence that corresponds to target.
// Exact StartTime wins, then the same local day, then the first row with
// the same ID. Falls back to target when the event is not in the slice.
func matchOpenEvent(events []event.Event, target event.Event) event.Event {
	if target.ID == 0 {
		return target
	}
	var dayMatch event.Event
	for _, e := range events {
		if e.ID != target.ID {
			continue
		}
		if !target.StartTime.IsZero() && e.StartTime.Equal(target.StartTime) {
			return e
		}
		if !target.StartTime.IsZero() && sameDay(e.StartTime.Local(), target.StartTime.Local()) {
			if dayMatch.ID == 0 {
				dayMatch = e
			}
			continue
		}
		if target.StartTime.IsZero() {
			return e
		}
	}
	if dayMatch.ID != 0 {
		return dayMatch
	}
	return target
}

// WithOpenEvent jumps every main view to ev's day and records the event so
// the first eventsLoadedMsg can select it and open the view dialog.
func (m Model) WithOpenEvent(ev event.Event) Model {
	if ev.ID == 0 {
		return m
	}
	m.pendingOpenEvent = ev
	if ev.StartTime.IsZero() {
		return m
	}
	t := ev.StartTime.Local()
	m.day.cursor = t
	m.week.cursor = t
	m.calendar.cursor = t
	m.calendar.month = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	m.agenda.cursor = t
	m.agenda = m.agenda.ResetWindow(t)
	return m
}
