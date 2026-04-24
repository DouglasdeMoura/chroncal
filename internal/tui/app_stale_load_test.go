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

// TestEventsLoadedMsg_IncrementalMergeAppendsToExisting verifies that a
// merge=true response for a slice abutting the currently-loaded range
// extends m.events and the loaded range, instead of replacing them.
// This is the core contract of the differential-loading path.
func TestEventsLoadedMsg_IncrementalMergeAppendsToExisting(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	// Initial window: [April 23, May 23). One event already loaded.
	apr23 := time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC)
	may23 := apr23.AddDate(0, 0, AgendaWindowDays)

	aprEv := event.Event{
		ID:        1,
		Title:     "April event",
		StartTime: time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC),
	}
	m := Model{
		viewMode:   viewAgenda,
		agenda:     NewAgendaModel(today),
		events:     []event.Event{aprEv},
		loadedFrom: apr23,
		loadedTo:   may23,
	}

	// Simulate backward expansion: windowStart moves to March 24.
	m.agenda.windowStart = apr23.AddDate(0, 0, -AgendaExpandStep).In(time.Local)

	marEv := event.Event{
		ID:        2,
		Title:     "March event",
		StartTime: time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC),
	}

	// Incremental response for the new slice only.
	mar24 := apr23.AddDate(0, 0, -AgendaExpandStep)
	updated, _ := m.Update(eventsLoadedMsg{
		from:   mar24,
		to:     apr23,
		merge:  true,
		events: []event.Event{marEv},
	})
	m = updated.(Model)

	if len(m.events) != 2 {
		t.Fatalf("expected merge to preserve April event and add March event; got %d events", len(m.events))
	}
	if !m.loadedFrom.Equal(mar24) {
		t.Fatalf("loadedFrom should have been extended to %v, got %v", mar24, m.loadedFrom)
	}
	if !m.loadedTo.Equal(may23) {
		t.Fatalf("loadedTo should be unchanged at %v, got %v", may23, m.loadedTo)
	}
}

// TestEventsLoadedMsg_IncrementalStaleDropped verifies that a merge=true
// response whose slice no longer abuts the loaded range is dropped — the
// user has jumped elsewhere and the delta is no longer meaningful.
func TestEventsLoadedMsg_IncrementalStaleDropped(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	apr23 := time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC)
	may23 := apr23.AddDate(0, 0, AgendaWindowDays)

	existing := event.Event{
		ID:        1,
		Title:     "April event",
		StartTime: time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC),
	}
	m := Model{
		viewMode:   viewAgenda,
		agenda:     NewAgendaModel(today),
		events:     []event.Event{existing},
		loadedFrom: apr23,
		loadedTo:   may23,
	}

	// Stale incremental result for a range that doesn't touch the currently
	// loaded [apr23, may23) — e.g. the user jumped to a far-away month
	// between firing the query and receiving the reply.
	janFrom := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	janTo := janFrom.AddDate(0, 0, AgendaWindowDays)
	strayEvent := event.Event{
		ID:        99,
		StartTime: janFrom.Add(72 * time.Hour),
	}
	updated, _ := m.Update(eventsLoadedMsg{
		from:   janFrom,
		to:     janTo,
		merge:  true,
		events: []event.Event{strayEvent},
	})
	m = updated.(Model)

	if len(m.events) != 1 || m.events[0].ID != existing.ID {
		t.Fatalf("stale incremental response should have been dropped; got %+v", m.events)
	}
}

// TestMergeEvents_DedupsByIDAndStartTime verifies the merge helper's
// dedup contract: a multi-day event returned by both the old slice and
// the new incremental slice must not appear twice in the merged list.
func TestMergeEvents_DedupsByIDAndStartTime(t *testing.T) {
	shared := event.Event{
		ID:        42,
		StartTime: time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC),
	}
	onlyOld := event.Event{
		ID:        1,
		StartTime: time.Date(2026, 4, 5, 9, 0, 0, 0, time.UTC),
	}
	onlyNew := event.Event{
		ID:        2,
		StartTime: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC),
	}

	out := mergeEvents(
		[]event.Event{onlyOld, shared},
		[]event.Event{shared, onlyNew},
	)
	if len(out) != 3 {
		t.Fatalf("expected 3 events after dedup, got %d: %+v", len(out), out)
	}
	seen := map[int64]int{}
	for _, e := range out {
		seen[e.ID]++
	}
	if seen[42] != 1 {
		t.Fatalf("shared event ID=42 deduped incorrectly; count=%d", seen[42])
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
