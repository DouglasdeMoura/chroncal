package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/model"
)

func newImportTestApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chroncal.db")
	t.Setenv("CHRONCAL_DB", dbPath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// TestImportComponentsContinuesPastItemFailure is the regression test for
// issue #141: a mid-file upsert failure used to `return` out of the import
// loop, so components after the bad one were silently dropped while the rows
// before it stayed committed. Importing must now skip only the failing
// component, persist the rest, and report the failure instead of aborting.
func TestImportComponentsContinuesPastItemFailure(t *testing.T) {
	ctx := context.Background()
	a := newImportTestApp(t)

	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	start := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	mkEvent := func(uid, title string, priority int64) event.Event {
		return event.Event{
			UID:       uid,
			Title:     title,
			StartTime: start,
			EndTime:   start.Add(time.Hour),
			Priority:  priority,
		}
	}

	// The middle event carries an out-of-range priority (CHECK constraint is
	// 0..9), so its upsert fails. The events on either side are valid.
	result := ical.ImportResult{
		Events: []event.Event{
			mkEvent("evt-before", "Before", 0),
			mkEvent("evt-bad", "Bad", 99),
			mkEvent("evt-after", "After", 0),
		},
	}

	summary := importComponents(ctx, a, cal.ID, &result)

	if summary.failed != 1 {
		t.Fatalf("summary.failed = %d, want 1", summary.failed)
	}
	if summary.newEvents != 2 {
		t.Fatalf("summary.newEvents = %d, want 2", summary.newEvents)
	}

	// The event after the failing one must still be persisted: this is the
	// core of the bug, where the early return discarded it.
	if _, err := a.Events.GetByUID(ctx, "evt-after"); err != nil {
		t.Fatalf("evt-after not persisted (import aborted early): %v", err)
	}
	if _, err := a.Events.GetByUID(ctx, "evt-before"); err != nil {
		t.Fatalf("evt-before not persisted: %v", err)
	}
	if _, err := a.Events.GetByUID(ctx, "evt-bad"); err == nil {
		t.Fatalf("evt-bad should not have been persisted")
	}

	// The failure must be surfaced, not silently swallowed.
	if !containsSubstring(result.Warnings, "Bad") {
		t.Fatalf("warnings = %v, want a warning mentioning the failed event", result.Warnings)
	}
}

// TestImportComponentsSurfacesChildFieldFailure covers the second half of
// issue #141: child-field imports (alarms, attendees, ...) only logged on
// failure, so partial child data was dropped while the command reported a
// clean success. A failing child import must now appear in the warnings.
func TestImportComponentsSurfacesChildFieldFailure(t *testing.T) {
	ctx := context.Background()
	a := newImportTestApp(t)

	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	start := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	evt := event.Event{
		UID:       "evt-bad-alarm",
		Title:     "Has bad alarm",
		StartTime: start,
		EndTime:   start.Add(time.Hour),
		// ACTION is constrained to AUDIO/DISPLAY/EMAIL, so this alarm fails
		// to attach even though the event itself is valid.
		Alarms: []model.Alarm{{
			Action:       "BOGUS",
			TriggerValue: "-PT15M",
			Related:      "START",
		}},
	}

	result := ical.ImportResult{Events: []event.Event{evt}}

	summary := importComponents(ctx, a, cal.ID, &result)

	// The event itself lands (failed counts only whole-component failures).
	if summary.failed != 0 {
		t.Fatalf("summary.failed = %d, want 0", summary.failed)
	}
	if _, err := a.Events.GetByUID(ctx, "evt-bad-alarm"); err != nil {
		t.Fatalf("event with bad alarm should still be imported: %v", err)
	}

	// But the dropped alarm must be reported instead of only logged.
	if !containsSubstring(result.Warnings, "alarms") {
		t.Fatalf("warnings = %v, want a warning about the dropped alarm", result.Warnings)
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
