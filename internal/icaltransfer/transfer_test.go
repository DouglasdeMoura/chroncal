package icaltransfer_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
	"github.com/douglasdemoura/chroncal/internal/model"
)

func newTestApp(t *testing.T) *app.App {
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

func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// TestParseFile_MixedComponentsAndWarnings confirms ParseFile produces a
// Preview with per-family counts and parse-time warnings for every supported
// iCal component family plus an unknown one. The exported component-family
// names are stable so callers can match them in error messages.
func TestParseFile_MixedComponentsAndWarnings(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:pf-event-1\r\nSUMMARY:Parse file event\r\n" +
		"DTSTART:20260421T090000Z\r\nDTEND:20260421T100000Z\r\nEND:VEVENT\r\n" +
		"BEGIN:VTODO\r\nUID:pf-todo-1\r\nSUMMARY:Parse file todo\r\nEND:VTODO\r\n" +
		"BEGIN:VJOURNAL\r\nUID:pf-journal-1\r\nSUMMARY:Parse file journal\r\nEND:VJOURNAL\r\n" +
		"BEGIN:VFREEBUSY\r\nUID:pf-fb-1\r\nDTSTART:20260421T090000Z\r\nDTEND:20260421T100000Z\r\n" +
		"FREEBUSY:20260421T090000Z/20260421T093000Z\r\nEND:VFREEBUSY\r\n" +
		// An unknown component is skipped with a warning.
		"BEGIN:VVENUE\r\nUID:pf-venue\r\nEND:VVENUE\r\n" +
		"END:VCALENDAR\r\n"

	path := filepath.Join(t.TempDir(), "mixed.ics")
	if err := os.WriteFile(path, []byte(ics), 0o600); err != nil {
		t.Fatalf("write ics: %v", err)
	}

	preview, err := icaltransfer.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if preview.Events != 1 {
		t.Errorf("preview.Events = %d, want 1", preview.Events)
	}
	if preview.Todos != 1 {
		t.Errorf("preview.Todos = %d, want 1", preview.Todos)
	}
	if preview.Journals != 1 {
		t.Errorf("preview.Journals = %d, want 1", preview.Journals)
	}
	if preview.FreeBusy != 1 {
		t.Errorf("preview.FreeBusy = %d, want 1", preview.FreeBusy)
	}
	if len(preview.Result.Events) != 1 || len(preview.Result.Todos) != 1 ||
		len(preview.Result.Journals) != 1 || len(preview.Result.FreeBusy) != 1 {
		t.Errorf("preview.Result families = ev=%d td=%d jr=%d fb=%d, want all 1",
			len(preview.Result.Events), len(preview.Result.Todos),
			len(preview.Result.Journals), len(preview.Result.FreeBusy))
	}
	if !containsSubstring(preview.Warnings, "VVENUE") {
		t.Errorf("preview.Warnings = %v, want one mentioning VVENUE", preview.Warnings)
	}
	if icaltransfer.FamilyEvent != "VEVENT" || icaltransfer.FamilyTodo != "VTODO" ||
		icaltransfer.FamilyJournal != "VJOURNAL" {
		t.Errorf("family names = %q/%q/%q, want VEVENT/VTODO/VJOURNAL",
			icaltransfer.FamilyEvent, icaltransfer.FamilyTodo, icaltransfer.FamilyJournal)
	}
}

// TestParseFile_OpenErrorIsWrapped guards the open-file error path: a missing
// file must surface as an "open file" error and must not leak an open handle.
func TestParseFile_OpenErrorIsWrapped(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.ics")
	_, err := icaltransfer.ParseFile(missing)
	if err == nil {
		t.Fatalf("ParseFile missing file: want error, got nil")
	}
	if !strings.Contains(err.Error(), "open file") {
		t.Errorf("error = %v, want an 'open file' wrap", err)
	}
}

// TestValidateDestination_ReadOnlyAndUnsupported exercises the destination
// capability guard: a read-only linked calendar rejects any present family,
// and a writable calendar with a narrow component list rejects families it
// does not advertise.
func TestValidateDestination_ReadOnlyAndUnsupported(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)

	cal, err := a.Calendars.Create(ctx, "Remote", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	accountResult, err := a.DB.ExecContext(ctx, `
		INSERT INTO accounts (name, server_url, auth_type, username)
		VALUES ('remote', 'https://cal.example.test/', 'basic', 'alice')`)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	accountID, _ := accountResult.LastInsertId()
	if _, err := a.DB.ExecContext(ctx, `
		UPDATE calendars
		SET account_id = ?, remote_url = '/cal/remote', remote_access = ?, remote_components = ?
		WHERE id = ?`, accountID, "read", "VEVENT", cal.ID); err != nil {
		t.Fatalf("link read-only: %v", err)
	}

	preview := icaltransfer.Preview{Result: ical.ImportResult{Events: []event.Event{{UID: "blocked"}}}}
	preview.Events = 1
	if err := icaltransfer.ValidateDestination(ctx, a, cal.ID, preview); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only error = %v, want read-only rejection", err)
	}

	if _, err := a.DB.ExecContext(ctx, `
		UPDATE calendars SET remote_access = 'write', remote_components = 'VTODO'
		WHERE id = ?`, cal.ID); err != nil {
		t.Fatalf("set VTODO-only: %v", err)
	}
	if err := icaltransfer.ValidateDestination(ctx, a, cal.ID, preview); err == nil ||
		!strings.Contains(err.Error(), icaltransfer.FamilyEvent) {
		t.Fatalf("unsupported error = %v, want %s rejection", err, icaltransfer.FamilyEvent)
	}
}

// TestImport_UIDUpsertNewUpdatedFailed covers the UID upsert contract: rows
// new to the database are reported as new, rows matched by UID are updated
// in place, and rows that fail their own upsert are counted as failed and
// skipped without aborting the rest of the import.
func TestImport_UIDUpsertNewUpdatedFailed(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	start := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	if _, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "evt-existing", CalendarID: cal.ID, Title: "Old title",
		StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed existing: %v", err)
	}

	mkEvent := func(uid, title string, priority int64) event.Event {
		return event.Event{
			UID: uid, Title: title, StartTime: start, EndTime: start.Add(time.Hour),
			Priority: priority,
		}
	}

	result := &ical.ImportResult{Events: []event.Event{
		mkEvent("evt-existing", "Updated title", 0), // update
		mkEvent("evt-new", "Brand new", 0),          // new
		mkEvent("evt-bad", "Bad", 99),               // fail (priority out of 0..9)
	}}
	summary := icaltransfer.Import(ctx, a, cal.ID, result)

	if summary.NewEvents != 1 {
		t.Errorf("NewEvents = %d, want 1", summary.NewEvents)
	}
	if summary.UpdatedEvents != 1 {
		t.Errorf("UpdatedEvents = %d, want 1", summary.UpdatedEvents)
	}
	if summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1", summary.Failed)
	}
	if len(summary.Events) != 2 {
		t.Errorf("len(Events) = %d, want 2 (failed one skipped)", len(summary.Events))
	}

	got, err := a.Events.GetByUID(ctx, "evt-existing")
	if err != nil {
		t.Fatalf("GetByUID evt-existing: %v", err)
	}
	if got.Title != "Updated title" {
		t.Errorf("evt-existing.Title = %q, want %q", got.Title, "Updated title")
	}
	if _, err := a.Events.GetByUID(ctx, "evt-new"); err != nil {
		t.Errorf("evt-new not persisted: %v", err)
	}
	if _, err := a.Events.GetByUID(ctx, "evt-bad"); err == nil {
		t.Errorf("evt-bad should not have been persisted")
	}
	if !containsSubstring(summary.Warnings, "Bad") {
		t.Errorf("summary.Warnings = %v, want one mentioning the failed event", summary.Warnings)
	}
}

// TestImport_ChildFieldFailureIsWarningNotFailed locks in the child-field
// warning semantics: a bad alarm must not count the parent event as failed,
// but the dropped alarm must surface as a warning.
func TestImport_ChildFieldFailureIsWarningNotFailed(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	start := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	evt := event.Event{
		UID: "evt-bad-alarm", Title: "Has bad alarm",
		StartTime: start, EndTime: start.Add(time.Hour),
		// ACTION is constrained to AUDIO/DISPLAY/EMAIL, so this alarm fails
		// to attach even though the event itself is valid.
		Alarms: []model.Alarm{{Action: "BOGUS", TriggerValue: "-PT15M", Related: "START"}},
	}
	result := &ical.ImportResult{Events: []event.Event{evt}}
	summary := icaltransfer.Import(ctx, a, cal.ID, result)

	if summary.Failed != 0 {
		t.Errorf("Failed = %d, want 0 (child failure must not count parent)", summary.Failed)
	}
	if _, err := a.Events.GetByUID(ctx, "evt-bad-alarm"); err != nil {
		t.Errorf("event with bad alarm should still import: %v", err)
	}
	if !containsSubstring(summary.Warnings, "alarms") {
		t.Errorf("summary.Warnings = %v, want one about dropped alarms", summary.Warnings)
	}
}

// TestValidateDestination_CrossCalendarReadOnlyUIDRejectedBeforeWrite locks
// in the cross-calendar UID guard: importing a UID that already lives in a
// read-only remote collection into a different calendar must be rejected
// before any write occurs, so the upsert cannot move data out of the
// read-only source.
func TestValidateDestination_CrossCalendarReadOnlyUIDRejectedBeforeWrite(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)

	source, err := a.Calendars.Create(ctx, "Remote", "", "")
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	accountResult, err := a.DB.ExecContext(ctx, `
		INSERT INTO accounts (name, server_url, auth_type, username)
		VALUES ('remote', 'https://cal.example.test/', 'basic', 'alice')`)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	accountID, _ := accountResult.LastInsertId()
	if _, err := a.DB.ExecContext(ctx, `
		UPDATE calendars
		SET account_id = ?, remote_url = '/cal/remote', remote_access = ?, remote_components = ?
		WHERE id = ?`, accountID, "read", "VEVENT", source.ID); err != nil {
		t.Fatalf("link source read-only: %v", err)
	}

	start := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	sourceEvt := event.Event{
		UID: "uid-cross-cal", Title: "Existing",
		StartTime: start, EndTime: start.Add(time.Hour),
	}
	if _, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: sourceEvt.UID, CalendarID: source.ID, Title: sourceEvt.Title,
		StartTime: sourceEvt.StartTime, EndTime: sourceEvt.EndTime,
	}); err != nil {
		t.Fatalf("seed source event: %v", err)
	}

	target, err := a.Calendars.Create(ctx, "Writable target", "", "")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	preview := icaltransfer.Preview{Result: ical.ImportResult{Events: []event.Event{sourceEvt}}}
	preview.Events = 1

	if err := icaltransfer.ValidateDestination(ctx, a, target.ID, preview); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatalf("cross-calendar error = %v, want read-only source rejection", err)
	}

	// Validation must have rejected before Import could write, so the row
	// still belongs to the source calendar.
	got, err := a.Events.GetByUID(ctx, "uid-cross-cal")
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	if got.CalendarID != source.ID {
		t.Errorf("event.CalendarID = %d, want %d (no write should have happened)",
			got.CalendarID, source.ID)
	}
}

func TestExportCalendarFileRoundTripsRelatedFields(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Archive", "#a6e3a1", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	start := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	saved, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "export-roundtrip", CalendarID: cal.ID, Title: "Standup",
		StartTime: start, EndTime: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := a.Events.ReplaceAlarms(ctx, saved.ID, []model.Alarm{{
		Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Reminder", Related: "START",
	}}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}

	path := filepath.Join(t.TempDir(), "archive.ics")
	summary, err := icaltransfer.ExportCalendarFile(ctx, a, cal.ID, cal.Name, path)
	if err != nil {
		t.Fatalf("ExportCalendarFile: %v", err)
	}
	if summary.Events != 1 || summary.Todos != 0 || summary.Journals != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	preview, err := icaltransfer.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile(export): %v", err)
	}
	if preview.Events != 1 || len(preview.Result.Events[0].Alarms) != 1 {
		t.Fatalf("round trip events=%d alarms=%d", preview.Events, len(preview.Result.Events[0].Alarms))
	}
}

func TestExportCalendarFileEmptyCalendarDoesNotCreateFile(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Empty", "#a6e3a1", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	path := filepath.Join(t.TempDir(), "empty.ics")
	_, err = icaltransfer.ExportCalendarFile(ctx, a, cal.ID, cal.Name, path)
	if err == nil || !strings.Contains(err.Error(), "no entries") {
		t.Fatalf("empty export error = %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("empty export created file: %v", statErr)
	}
}
