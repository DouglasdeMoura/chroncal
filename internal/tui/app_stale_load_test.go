package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestEventsLoadedMsg_StaleDropPreservesAgendaWindow reproduces the agenda
// "data does not load" bug: rapid month navigation fires several in-flight
// queries, and a late reply for an older window must not overwrite rows for
// the current window with an empty set.
func TestEventsLoadedMsg_StaleDropPreservesAgendaWindow(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	agenda := NewAgendaModel(today)
	// Current window is the default (starts today). Simulate the user having
	// navigated to March — agenda.ResetWindow is what the handler calls when
	// AgendaCursorChangedMsg fires.
	currentStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local)
	agenda = agenda.ResetWindow(currentStart)

	m := Model{viewMode: viewAgenda, agenda: agenda}

	march1UTC := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	march31UTC := march1UTC.AddDate(0, 0, AgendaWindowDays)

	marchEvent := event.Event{
		ID:        1,
		Title:     "March planning",
		StartTime: time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
	}
	aprilEvent := event.Event{
		ID:        2,
		Title:     "April demo",
		StartTime: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
	}

	// Fresh response for the active March window lands first.
	updated, _ := m.Update(eventsLoadedMsg{
		from:   march1UTC,
		to:     march31UTC,
		events: []event.Event{marchEvent},
	})
	m = updated.(Model)

	if len(m.events) != 1 || m.events[0].ID != marchEvent.ID {
		t.Fatalf("expected March event to be applied, got %+v", m.events)
	}

	// Stale response from an earlier load for the April window arrives late.
	// It must be dropped — not stomp March data with unrelated events.
	april1UTC := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	apr30UTC := april1UTC.AddDate(0, 0, AgendaWindowDays)
	updated, _ = m.Update(eventsLoadedMsg{
		from:   april1UTC,
		to:     apr30UTC,
		events: []event.Event{aprilEvent},
	})
	m = updated.(Model)

	if len(m.events) != 1 || m.events[0].ID != marchEvent.ID {
		t.Fatalf("stale April response should have been dropped; m.events = %+v", m.events)
	}
}

// TestExpectedEventRange_AgendaTracksWindow verifies the helper reports the
// current agenda window exactly, so the stale-drop guard compares equal
// times (not dates shifted by timezone rounding).
func TestExpectedEventRange_AgendaTracksWindow(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	agenda := NewAgendaModel(today).ResetWindow(
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local),
	)
	m := Model{viewMode: viewAgenda, agenda: agenda}

	from, to := m.expectedEventRange()
	wantFrom := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	wantTo := wantFrom.AddDate(0, 0, AgendaWindowDays)
	if !from.Equal(wantFrom) {
		t.Fatalf("from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Fatalf("to = %v, want %v", to, wantTo)
	}
}
