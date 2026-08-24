package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestDeleteScopeValidationBlocksAndSurfaces drives the recurring-delete
// choice dialog for a scope time that matches no generated instance. The
// storage call must never run, and the validation error must travel through
// eventDeletedMsg so the toast surfaces it. Regression for the thermos
// round-2 finding that scopeErr was computed but never checked, so the
// delete ran anyway.
func TestDeleteScopeValidationBlocksAndSurfaces(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()

	cal, err := a.Calendars.Create(ctx, "Work", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	ev, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID:     cal.ID,
		Title:          "Weekly",
		StartTime:      start,
		EndTime:        start.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=WE",
	})
	if err != nil {
		t.Fatalf("event create: %v", err)
	}

	next, _ := m.handleEventDelete(EventDeleteMsg{Event: ev})
	m = next.(Model)
	if !m.choiceOpen {
		t.Fatal("recurring delete did not arm the scope dialog")
	}
	// Point the armed scope at a non-instance slot without touching storage.
	ev.StartTime = start.Add(time.Hour)
	m.pending.target.ev = ev

	next, cmd := m.handleChoiceDialogResult(ChoiceDialogResultMsg{Choice: 0}) // This event
	after := next.(Model)
	if after.confirmOpen {
		t.Fatal("the failed scope left the choice dialog open")
	}
	if cmd == nil {
		t.Fatal("expected a command carrying the validation result")
	}
	res := cmd()
	done, ok := res.(eventDeletedMsg)
	if !ok {
		t.Fatalf("choice dispatched %T, want eventDeletedMsg", res)
	}
	if done.err == nil || !strings.Contains(done.err.Error(), "no occurrence matches") {
		t.Fatalf("err = %v, want the no-occurrence validation error", done.err)
	}

	// Storage is untouched: the master row survives without any EXDATE.
	got, gerr := a.Events.GetByUID(ctx, ev.UID)
	if gerr != nil {
		t.Fatalf("master vanished: %v", gerr)
	}
	if strings.TrimSpace(got.ExDates) != "" {
		t.Fatalf("phantom EXDATE written: %q", got.ExDates)
	}
}
