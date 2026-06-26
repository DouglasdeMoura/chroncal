package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/config"
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

// TestImportJSONOutputStaysValidWhenPushNotes is the regression test for
// issue #255: `ical import --output json` passed the command's stdout writer
// to the opportunistic push seam unconditionally, so the human-readable
// "Synced to ..." note was appended after the JSON object, producing invalid
// JSON on stdout. In JSON mode the note must go to io.Discard (matching the
// event/todo/journal write paths) so stdout stays parseable.
func TestImportJSONOutputStaysValidWhenPushNotes(t *testing.T) {
	ctx := context.Background()
	a := newImportTestApp(t)

	if _, err := a.Calendars.Create(ctx, "Work", "", ""); err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	icsPath := filepath.Join(t.TempDir(), "in.ics")
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VTODO\r\nUID:import-json-1\r\nSUMMARY:Imported task\r\nEND:VTODO\r\n" +
		"END:VCALENDAR\r\n"
	if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("write ics: %v", err)
	}

	// Simulate a CalDAV-linked calendar that produces a pushable change: the
	// real seam writes "Synced to ..." to the writer it is handed. In JSON
	// mode it must be handed io.Discard, so this note must never reach stdout.
	const syncNote = "Synced to Work · pushed 1 · deleted 0\n"
	prev := pushCalendarAfterWrite
	pushCalendarAfterWrite = func(_ *app.App, _ int64, w io.Writer) {
		io.WriteString(w, syncNote)
	}
	t.Cleanup(func() { pushCalendarAfterWrite = prev })

	prevFmt := outputFmt
	outputFmt = "json"
	t.Cleanup(func() { outputFmt = prevFmt })

	root := &cobra.Command{
		Use: "chroncal-test",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cfg = config.Load()
			return nil
		},
	}
	root.AddCommand(icalCmd())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ical", "import", icsPath, "--calendar", "Work"})
	if err := root.Execute(); err != nil {
		t.Fatalf("ical import: %v", err)
	}

	if strings.Contains(out.String(), "Synced to") {
		t.Fatalf("sync note leaked into JSON stdout:\n%s", out.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\noutput:\n%s", err, out.String())
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
