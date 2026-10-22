package icaltransfer_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
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

// TestParseFile_OpenErrorIsWrapped guards the open-file error path. A file that
// is gone must surface as an "open file" error. It must not leak an open handle.
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
// capability guard. A read-only linked calendar rejects any present family.
// A writable calendar with a narrow component list rejects families it
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

// TestImport_UIDUpsertNewUpdatedFailed covers the UID upsert contract. Rows
// new to the database are reported as new. Rows matched by UID are updated
// in place. Rows that fail their own upsert are counted as failed. They are
// skipped. The rest of the import is not aborted.
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
// warning semantics. A bad alarm must not count the parent event as failed.
// The dropped alarm must surface as a warning.
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
		// The service boundary rejects a RELATED outside
		// model.AlarmRelatedValues with model.ErrInvalidAlarm (issue
		// #578), so this alarm fails to attach even though the event
		// itself is valid. The action no longer forces the failure:
		// since issue #579 the tables accept any non-empty action.
		Alarms: []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "BOGUS"}},
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

// TestImport_ReimportRemovesStaleChildren locks in the re-import contract.
// An import replaces the child collections of a row. It does not merge
// them. A re-import that omits a child must remove the stored child. The
// old len>0 guard kept stale alarms and attendees on the row, so a
// removal on the source never reached the local copy.
func TestImport_ReimportRemovesStaleChildren(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	start := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	attendee := model.Attendee{
		Email: "user@example.com", Name: "User",
		RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT",
	}
	importAll := func(withChildren bool) icaltransfer.Summary {
		evt := event.Event{
			UID: "evt-children", Title: "Reimport",
			StartTime: start, EndTime: start.Add(time.Hour),
		}
		td := todo.Todo{UID: "todo-children", Summary: "Reimport todo"}
		jr := journal.Journal{UID: "jr-children", Summary: "Reimport journal"}
		if withChildren {
			evt.Alarms = []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "START"}}
			evt.Attendees = []model.Attendee{attendee}
			td.Alarms = []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT30M", Related: "START"}}
			jr.Attendees = []model.Attendee{attendee}
		}
		return icaltransfer.Import(ctx, a, cal.ID, &ical.ImportResult{
			Events:   []event.Event{evt},
			Todos:    []todo.Todo{td},
			Journals: []journal.Journal{jr},
		})
	}

	if s := importAll(true); s.Failed != 0 {
		t.Fatalf("first import Failed = %d, want 0 (warnings: %v)", s.Failed, s.Warnings)
	}
	if s := importAll(false); s.Failed != 0 {
		t.Fatalf("re-import Failed = %d, want 0 (warnings: %v)", s.Failed, s.Warnings)
	}

	evt, err := a.Events.GetByUID(ctx, "evt-children")
	if err != nil {
		t.Fatalf("GetByUID event: %v", err)
	}
	if alarms, err := a.Events.ListAlarms(ctx, evt.ID); err != nil || len(alarms) != 0 {
		t.Errorf("event alarms after re-import = %v (err %v), want none", alarms, err)
	}
	if atts, err := a.Events.ListAttendees(ctx, evt.ID); err != nil || len(atts) != 0 {
		t.Errorf("event attendees after re-import = %v (err %v), want none", atts, err)
	}

	td, err := a.Todos.GetByUID(ctx, "todo-children")
	if err != nil {
		t.Fatalf("GetByUID todo: %v", err)
	}
	if alarms, err := a.Todos.ListAlarms(ctx, td.ID); err != nil || len(alarms) != 0 {
		t.Errorf("todo alarms after re-import = %v (err %v), want none", alarms, err)
	}

	jr, err := a.Journals.GetByUID(ctx, "jr-children")
	if err != nil {
		t.Fatalf("GetByUID journal: %v", err)
	}
	if atts, err := a.Journals.ListAttendees(ctx, jr.ID); err != nil || len(atts) != 0 {
		t.Errorf("journal attendees after re-import = %v (err %v), want none", atts, err)
	}
}

// TestValidateDestination_CrossCalendarEventUIDAllowed locks in issue #756.
// An event UID that already lives on a read-only calendar can still import
// into a different writable calendar. The upsert inserts a second copy. It
// does not move the source row.
func TestValidateDestination_CrossCalendarEventUIDAllowed(t *testing.T) {
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

	if err := icaltransfer.ValidateDestination(ctx, a, target.ID, preview); err != nil {
		t.Fatalf("ValidateDestination: %v", err)
	}
	summary := icaltransfer.Import(ctx, a, target.ID, &preview.Result)
	if summary.Failed != 0 || summary.NewEvents != 1 {
		t.Fatalf("import Failed=%d NewEvents=%d, want Failed=0 NewEvents=1",
			summary.Failed, summary.NewEvents)
	}
	src, err := a.Events.GetByCalendarUID(ctx, source.ID, "uid-cross-cal")
	if err != nil {
		t.Fatalf("source copy: %v", err)
	}
	if src.Title != "Existing" {
		t.Errorf("source title = %q, want Existing", src.Title)
	}
	dst, err := a.Events.GetByCalendarUID(ctx, target.ID, "uid-cross-cal")
	if err != nil {
		t.Fatalf("destination copy: %v", err)
	}
	if dst.ID == src.ID {
		t.Fatalf("import reused id %d; each calendar must keep its own copy", src.ID)
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

// TestImport_FirstTimeOverrideCountsAsNew extends the UID upsert contract to
// overrides. A first-time override shares the master's UID, so a plain
// GetByUID matched it and counted it as updated. The override lookup must
// match its own row through its recurrence_id.
func TestImport_FirstTimeOverrideCountsAsNew(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	start := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	if _, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "evt-series", CalendarID: cal.ID, Title: "Master",
		StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed master: %v", err)
	}

	overrideAt := start.Add(24 * time.Hour)
	result := &ical.ImportResult{Events: []event.Event{{
		UID: "evt-series", Title: "Moved instance",
		StartTime: overrideAt, EndTime: overrideAt.Add(time.Hour),
		RecurrenceID: overrideAt.Format(time.RFC3339),
	}}}
	summary := icaltransfer.Import(ctx, a, cal.ID, result)

	if summary.NewEvents != 1 {
		t.Errorf("NewEvents = %d, want 1 (first-time override is new, not an update of the master)", summary.NewEvents)
	}
	if summary.UpdatedEvents != 0 {
		t.Errorf("UpdatedEvents = %d, want 0", summary.UpdatedEvents)
	}
	if _, err := a.Events.GetByUIDAndRecurrenceID(ctx, "evt-series", overrideAt.Format(time.RFC3339)); err != nil {
		t.Errorf("override row not persisted: %v", err)
	}
}

// TestImport_ClearsThePendingDeleteOfTheUID imports a UID that a delete
// tombstoned in a synced calendar. The import must clear the tombstone, so
// the tombstone pass does not delete the server object under the imported
// rows (issue #841).
func TestImport_ClearsThePendingDeleteOfTheUID(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()

	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cal, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Synced"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if err := a.Queries.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: cal.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/cal/synced/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	const uid = "import-after-delete"
	if err := a.Queries.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: cal.ID, Uid: uid, OwnerType: "event", RemoteUrl: "/cal/synced/" + uid + ".ics", Etag: "e1", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: cal.ID, Uid: uid, RemoteUrl: "/cal/synced/" + uid + ".ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{Events: []event.Event{{
		UID: uid, Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour),
	}}}
	summary := icaltransfer.Import(ctx, a, cal.ID, result)
	if summary.Failed != 0 || len(summary.Events) != 1 {
		t.Fatalf("import summary = %+v, warnings %v; want one event", summary, result.Warnings)
	}
	tombstones, err := a.Queries.ListTombstonesByCalendar(ctx, cal.ID)
	if err != nil || len(tombstones) != 0 {
		t.Fatalf("tombstones after the import = %v, %v; want none", tombstones, err)
	}
}

// TestImport_SkipsThePendingDeleteClearOnALocalCalendar imports into a
// calendar without an account. A local calendar has no pending deletes. The
// import must not touch the tombstones table for it, and a tombstone row of
// another calendar stays.
func TestImport_SkipsThePendingDeleteClearOnALocalCalendar(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()

	local, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Local"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	other, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Other"})
	if err != nil {
		t.Fatalf("CreateCalendar other: %v", err)
	}
	const uid = "local-import"
	if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: other.ID, Uid: uid, RemoteUrl: "/cal/other/" + uid + ".ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{Events: []event.Event{{UID: uid, Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour)}}}
	if summary := icaltransfer.Import(ctx, a, local.ID, result); summary.Failed != 0 || len(result.Warnings) != 0 {
		t.Fatalf("import = %+v, warnings %v; want a clean import", summary, result.Warnings)
	}
	if tombstones, err := a.Queries.ListTombstonesByCalendar(ctx, other.ID); err != nil || len(tombstones) != 1 {
		t.Fatalf("tombstones of the other calendar = %v, %v; want the one row kept", tombstones, err)
	}
}

// TestImport_ClearsThePendingDeletesOfManyUIDs imports more UIDs than a
// SQLite bind list can hold at once. The clear reads the tombstones of the
// calendar and deletes the ones of an imported UID. Every pending delete of
// an imported UID goes, and the other tombstones stay.
func TestImport_ClearsThePendingDeletesOfManyUIDs(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()

	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cal, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Synced"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if err := a.Queries.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: cal.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/cal/synced/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	const total = 1100
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{}
	for i := range total {
		uid := fmt.Sprintf("many-%04d", i)
		result.Events = append(result.Events, event.Event{UID: uid, Title: "Many", StartTime: start, EndTime: start.Add(time.Hour)})
	}
	for _, uid := range []string{"many-0000", "many-0999", "many-1099", "not-imported"} {
		if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
			CalendarID: cal.ID, Uid: uid, RemoteUrl: "/cal/synced/" + uid + ".ics",
		}); err != nil {
			t.Fatalf("CreateTombstone %s: %v", uid, err)
		}
	}

	summary := icaltransfer.Import(ctx, a, cal.ID, result)
	if summary.Failed != 0 || len(summary.Events) != total {
		t.Fatalf("import summary = failed %d, events %d; want %d events", summary.Failed, len(summary.Events), total)
	}
	for _, w := range result.Warnings {
		if strings.Contains(w, "pending delete") {
			t.Fatalf("warnings = %v, want no clear failure", result.Warnings)
		}
	}
	tombstones, err := a.Queries.ListTombstonesByCalendar(ctx, cal.ID)
	if err != nil || len(tombstones) != 1 || tombstones[0].Uid != "not-imported" {
		t.Fatalf("tombstones after the import = %v, %v; want only the one of a UID that was not imported", tombstones, err)
	}
}

// TestImport_FailedClearRollsTheUpsertBack imports a UID into a synced
// calendar while the clear of its pending delete fails. The clear runs in
// the transaction of the upsert, so the item must not land, and the
// failure must reach the summary as a warning.
func TestImport_FailedClearRollsTheUpsertBack(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()

	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cal, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Synced"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if err := a.Queries.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: cal.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/cal/synced/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx, `CREATE TRIGGER fail_clear BEFORE DELETE ON tombstones BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: cal.ID, Uid: "fail-clear", RemoteUrl: "/cal/synced/fail-clear.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{Events: []event.Event{{UID: "fail-clear", Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour)}}}
	summary := icaltransfer.Import(ctx, a, cal.ID, result)
	if summary.Failed != 1 || len(summary.Events) != 0 {
		t.Fatalf("import summary = failed %d, events %d; want one failure", summary.Failed, len(summary.Events))
	}
	if !containsSubstring(summary.Warnings, "clear the pending delete") {
		t.Fatalf("summary warnings = %v, want the failed clear", summary.Warnings)
	}
	if _, err := a.Events.GetByCalendarUID(ctx, cal.ID, "fail-clear"); err == nil {
		t.Fatal("the item landed although its clear failed")
	}
	if tombstones, err := a.Queries.ListTombstonesByCalendar(ctx, cal.ID); err != nil || len(tombstones) != 1 {
		t.Fatalf("tombstones = %v, %v; want the tombstone kept", tombstones, err)
	}
}

// TestImport_OverrideKeepsTheSeriesDelete imports only an override of a
// series whose delete waits in a synced calendar. The override does not
// bring back the series, so the tombstone must stay, and the import must
// warn.
func TestImport_OverrideKeepsTheSeriesDelete(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()

	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cal, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Synced"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if err := a.Queries.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: cal.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/cal/synced/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: cal.ID, Uid: "series-uid", RemoteUrl: "/cal/synced/series-uid.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{Events: []event.Event{{
		UID: "series-uid", RecurrenceID: "2026-04-03T10:00:00Z", Title: "Override", StartTime: start, EndTime: start.Add(time.Hour),
	}}}
	summary := icaltransfer.Import(ctx, a, cal.ID, result)
	if summary.Failed != 0 {
		t.Fatalf("import summary = %+v, warnings %v; want no failure", summary, summary.Warnings)
	}
	if !containsSubstring(summary.Warnings, "the series of series-uid is deleted; the override stays local") {
		t.Fatalf("summary warnings = %v, want the deleted series", summary.Warnings)
	}
	if tombstones, err := a.Queries.ListTombstonesByCalendar(ctx, cal.ID); err != nil || len(tombstones) != 1 {
		t.Fatalf("tombstones = %v, %v; want the series delete kept", tombstones, err)
	}
}

// syncedPair returns two calendars of one account.
func syncedPair(t *testing.T, a *app.App) (calA, calB storage.Calendar) {
	t.Helper()
	ctx := context.Background()
	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	link := func(name string) storage.Calendar {
		cal, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
		if err != nil {
			t.Fatalf("CreateCalendar: %v", err)
		}
		if err := a.Queries.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID: cal.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/cal/" + name + "/"),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount: %v", err)
		}
		return cal
	}
	return link("a"), link("b")
}

// TestImport_KeepsAPendingMove imports an event and a todo into a calendar
// whose move tombstone of the same UID waits for a move to another
// calendar. The import must refuse both, keep the tombstones, warn with
// the target calendar, and leave the moved todo where it is.
func TestImport_KeepsAPendingMove(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	calA, calB := syncedPair(t, a)

	for _, uid := range []string{"moved-event", "moved-todo"} {
		if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
			CalendarID: calA.ID, Uid: uid, RemoteUrl: "/cal/a/" + uid + ".ics", MovedToCalendarID: &calB.ID,
		}); err != nil {
			t.Fatalf("CreateTombstone %s: %v", uid, err)
		}
	}
	moved, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "moved-todo", CalendarID: calB.ID, Summary: "Moved"})
	if err != nil {
		t.Fatalf("UpsertByUID todo in B: %v", err)
	}

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{
		Events: []event.Event{{UID: "moved-event", Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour)}},
		Todos:  []todo.Todo{{UID: "moved-todo", Summary: "Imported"}},
	}
	summary := icaltransfer.Import(ctx, a, calA.ID, result)
	if summary.Failed != 2 {
		t.Fatalf("import summary = failed %d, warnings %v; want two refusals", summary.Failed, summary.Warnings)
	}
	if !containsSubstring(summary.Warnings, `moved-event waits for its move to calendar "b"; the import keeps the move`) {
		t.Fatalf("summary warnings = %v, want the pending move of the event", summary.Warnings)
	}
	if tombstones, err := a.Queries.ListTombstonesByCalendar(ctx, calA.ID); err != nil || len(tombstones) != 2 {
		t.Fatalf("tombstones = %v, %v; want both move tombstones kept", tombstones, err)
	}
	if got, err := a.Todos.Get(ctx, moved.ID); err != nil || got.CalendarID != calB.ID || got.Summary != "Moved" {
		t.Fatalf("moved todo = %+v, %v; want it unchanged in B", got, err)
	}
}

// TestImport_RefusesACrossCalendarMoveOfAMovedTodo imports a todo override
// into the source of a pending move, and a todo master into a third
// calendar. The key of a todo spans calendars, so each upsert would move a
// row of the pending move. The import must refuse both and leave the rows
// in the target. The pending-move guard refuses the override first.
func TestImport_RefusesACrossCalendarMoveOfAMovedTodo(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	calA, calB := syncedPair(t, a)
	calC, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "c"})
	if err != nil {
		t.Fatalf("CreateCalendar c: %v", err)
	}
	linkedA, err := a.Queries.GetCalendar(ctx, calA.ID)
	if err != nil {
		t.Fatalf("GetCalendar a: %v", err)
	}
	if err := a.Queries.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: calC.ID, AccountID: linkedA.AccountID, RemoteUrl: storage.StringToNullable("/cal/c/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount c: %v", err)
	}
	if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calA.ID, Uid: "moved-todo", RemoteUrl: "/cal/a/moved-todo.ics", MovedToCalendarID: &calB.ID,
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	master, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "moved-todo", CalendarID: calB.ID, Summary: "Moved", RecurrenceRule: "FREQ=DAILY;COUNT=3", DueDate: "2026-04-03T10:00:00Z"})
	if err != nil {
		t.Fatalf("UpsertByUID master in B: %v", err)
	}
	override, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "moved-todo", CalendarID: calB.ID, Summary: "Moved override", RecurrenceID: "2026-04-04T10:00:00Z", DueDate: "2026-04-04T10:00:00Z"})
	if err != nil {
		t.Fatalf("UpsertByUID override in B: %v", err)
	}

	overrideImport := &ical.ImportResult{Todos: []todo.Todo{{UID: "moved-todo", Summary: "Imported override", RecurrenceID: "2026-04-04T10:00:00Z", DueDate: "2026-04-04T10:00:00Z"}}}
	if summary := icaltransfer.Import(ctx, a, calA.ID, overrideImport); summary.Failed != 1 || !containsSubstring(summary.Warnings, "waits for its move") {
		t.Fatalf("override import into A = failed %d, warnings %v; want a refusal", summary.Failed, summary.Warnings)
	}
	masterImport := &ical.ImportResult{Todos: []todo.Todo{{UID: "moved-todo", Summary: "Imported master", DueDate: "2026-04-03T10:00:00Z"}}}
	if summary := icaltransfer.Import(ctx, a, calC.ID, masterImport); summary.Failed != 1 || !containsSubstring(summary.Warnings, "the import does not move the row") {
		t.Fatalf("master import into C = failed %d, warnings %v; want a refusal", summary.Failed, summary.Warnings)
	}
	for _, id := range []int64{master.ID, override.ID} {
		if got, err := a.Todos.Get(ctx, id); err != nil || got.CalendarID != calB.ID || !strings.HasPrefix(got.Summary, "Moved") {
			t.Fatalf("todo %d = %+v, %v; want it unchanged in B", id, got, err)
		}
	}
}

// TestImport_RefusesAnOverrideIntoAMoveSource imports an event override
// into the source calendar of a pending move. The guard applies to an
// override too, so the import refuses it and names the target.
func TestImport_RefusesAnOverrideIntoAMoveSource(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	calA, calB := syncedPair(t, a)
	if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calA.ID, Uid: "moved-series", RemoteUrl: "/cal/a/moved-series.ics", MovedToCalendarID: &calB.ID,
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	start := time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{Events: []event.Event{{
		UID: "moved-series", Title: "Override", StartTime: start, EndTime: start.Add(time.Hour), RecurrenceID: start.Format(time.RFC3339),
	}}}
	summary := icaltransfer.Import(ctx, a, calA.ID, result)
	if summary.Failed != 1 || !containsSubstring(summary.Warnings, `waits for its move to calendar "b"`) {
		t.Fatalf("import = failed %d, warnings %v; want a refusal that names the target", summary.Failed, summary.Warnings)
	}
	if evts, err := a.Events.ListSeriesOverrides(ctx, calA.ID, "moved-series"); err != nil || len(evts) != 0 {
		t.Fatalf("overrides = %v, %v; want none", evts, err)
	}
}

// TestImport_FailsWhenTheCalendarLookupFails imports into a calendar that
// does not exist. The lookup of the calendar fails, so the import fails
// every item and does not guess the sync state.
func TestImport_FailsWhenTheCalendarLookupFails(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	start := time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{
		Events: []event.Event{{UID: "lookup-evt", Title: "E", StartTime: start, EndTime: start.Add(time.Hour)}},
		Todos:  []todo.Todo{{UID: "lookup-todo", Summary: "T"}},
	}
	summary := icaltransfer.Import(ctx, a, 999999, result)
	if summary.Failed != 2 || !containsSubstring(summary.Warnings, "check the calendar of the import") {
		t.Fatalf("import = failed %d, warnings %v; want two failures and the lookup error", summary.Failed, summary.Warnings)
	}
}

// TestImport_RefusesATodoOfAnotherSyncedCalendar imports a todo whose row
// lives in another synced calendar, with no pending move. The upsert
// would move the row with no move record, so the import must refuse it.
// A todo from a local calendar still moves.
func TestImport_RefusesATodoOfAnotherSyncedCalendar(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	calA, calB := syncedPair(t, a)
	local, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar local: %v", err)
	}
	inB, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "synced-todo", CalendarID: calB.ID, Summary: "In B"})
	if err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	inLocal, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "local-todo", CalendarID: local.ID, Summary: "In local"})
	if err != nil {
		t.Fatalf("UpsertByUID in local: %v", err)
	}
	result := &ical.ImportResult{Todos: []todo.Todo{{UID: "synced-todo", Summary: "Imported"}, {UID: "local-todo", Summary: "Imported local"}}}
	summary := icaltransfer.Import(ctx, a, calA.ID, result)
	if summary.Failed != 1 || !containsSubstring(summary.Warnings, `synced-todo lives in calendar "b"; move it there or delete it first`) {
		t.Fatalf("import = failed %d, warnings %v; want one refusal", summary.Failed, summary.Warnings)
	}
	if got, err := a.Todos.Get(ctx, inB.ID); err != nil || got.CalendarID != calB.ID || got.Summary != "In B" {
		t.Fatalf("todo in B = %+v, %v; want it unchanged", got, err)
	}
	if got, err := a.Todos.Get(ctx, inLocal.ID); err != nil || got.CalendarID != calA.ID {
		t.Fatalf("todo from local = %+v, %v; want it moved to A", got, err)
	}
}

// TestImport_RefusesATodoFromTheTrashOfAnotherCalendar imports a todo
// into a synced calendar while its row is in the trash of another synced
// calendar. The import holds no lock of that calendar, so it must refuse
// and write nothing there.
func TestImport_RefusesATodoFromTheTrashOfAnotherCalendar(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	calA, calB := syncedPair(t, a)
	inB, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "trashed-todo", CalendarID: calB.ID, Summary: "In B"})
	if err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := a.Queries.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calB.ID, Uid: "trashed-todo", OwnerType: "todo", RemoteUrl: "/cal/b/trashed-todo.ics", Etag: "e1", SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := a.Queries.SoftDeleteTodo(ctx, inB.ID); err != nil {
		t.Fatalf("SoftDeleteTodo: %v", err)
	}
	result := &ical.ImportResult{Todos: []todo.Todo{{UID: "trashed-todo", Summary: "Imported"}}}
	summary := icaltransfer.Import(ctx, a, calA.ID, result)
	if summary.Failed != 1 || !containsSubstring(summary.Warnings, `trashed-todo is in the trash of calendar "b"; restore it there or purge it first`) {
		t.Fatalf("import = failed %d, warnings %v; want a refusal", summary.Failed, summary.Warnings)
	}
	if ts, err := a.Queries.ListTombstonesByCalendar(ctx, calB.ID); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones of B = %v, %v; want no write there", ts, err)
	}
}

// TestImport_RefusesASyncedTodoIntoALocalCalendar imports a todo into a
// local calendar while its live row lives in a synced calendar. The upsert
// would move the row out of the synced calendar with no move record, so
// the import must refuse it there too.
func TestImport_RefusesASyncedTodoIntoALocalCalendar(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	_, calB := syncedPair(t, a)
	local, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar local: %v", err)
	}
	inB, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "synced-todo", CalendarID: calB.ID, Summary: "In B"})
	if err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	result := &ical.ImportResult{Todos: []todo.Todo{{UID: "synced-todo", Summary: "Imported"}}}
	summary := icaltransfer.Import(ctx, a, local.ID, result)
	if summary.Failed != 1 || !containsSubstring(summary.Warnings, `synced-todo lives in calendar "b"; move it there or delete it first`) {
		t.Fatalf("import = failed %d, warnings %v; want a refusal", summary.Failed, summary.Warnings)
	}
	if got, err := a.Todos.Get(ctx, inB.ID); err != nil || got.CalendarID != calB.ID || got.Summary != "In B" {
		t.Fatalf("todo = %+v, %v; want it unchanged in B", got, err)
	}
}

// TestImport_RefusesATrashedSyncedTodoIntoALocalCalendar imports a todo
// into a local calendar while its row is in the trash of a synced
// calendar. A local import holds no lock of the synced calendar, so it
// must refuse and write nothing there.
func TestImport_RefusesATrashedSyncedTodoIntoALocalCalendar(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	_, calB := syncedPair(t, a)
	local, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar local: %v", err)
	}
	inB, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "trashed-todo", CalendarID: calB.ID, Summary: "In B"})
	if err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := a.Queries.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calB.ID, Uid: "trashed-todo", OwnerType: "todo", RemoteUrl: "/cal/b/trashed-todo.ics", Etag: "e1", SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := a.Queries.SoftDeleteTodo(ctx, inB.ID); err != nil {
		t.Fatalf("SoftDeleteTodo: %v", err)
	}
	result := &ical.ImportResult{Todos: []todo.Todo{{UID: "trashed-todo", Summary: "Imported"}}}
	summary := icaltransfer.Import(ctx, a, local.ID, result)
	if summary.Failed != 1 || !containsSubstring(summary.Warnings, `trashed-todo is in the trash of calendar "b"; restore it there or purge it first`) {
		t.Fatalf("import = failed %d, warnings %v; want a refusal", summary.Failed, summary.Warnings)
	}
	if ts, err := a.Queries.ListTombstonesByCalendar(ctx, calB.ID); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones of B = %v, %v; want no write there", ts, err)
	}
}

// TestImport_RefusedTodoWritesNoCategory imports a todo with a category
// while its live row lives in another synced calendar. A trigger fails
// every category insert. The import must refuse with the cross-calendar
// warning before it writes the row or its category, so the trigger never
// fires.
func TestImport_RefusedTodoWritesNoCategory(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	calA, calB := syncedPair(t, a)
	if _, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "synced-todo", CalendarID: calB.ID, Summary: "In B"}); err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx, `CREATE TRIGGER no_category BEFORE INSERT ON todo_categories
		BEGIN SELECT RAISE(ABORT, 'category write'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	result := &ical.ImportResult{Todos: []todo.Todo{{UID: "synced-todo", Summary: "Imported", Categories: "work"}}}
	summary := icaltransfer.Import(ctx, a, calA.ID, result)
	if summary.Failed != 1 || !containsSubstring(summary.Warnings, `lives in calendar "b"`) || containsSubstring(summary.Warnings, "category write") {
		t.Fatalf("import = failed %d, warnings %v; want the cross-calendar refusal and no category write", summary.Failed, summary.Warnings)
	}
}

// TestImport_PendingMoveRefusalWritesNoCategory imports an event with a
// category into the source of a pending move. A trigger fails every event
// category insert. The pending-move refusal must come before the upsert,
// so the trigger never fires.
func TestImport_PendingMoveRefusalWritesNoCategory(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	calA, calB := syncedPair(t, a)
	if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calA.ID, Uid: "moved-evt", RemoteUrl: "/cal/a/moved-evt.ics", MovedToCalendarID: &calB.ID,
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx, `CREATE TRIGGER no_category BEFORE INSERT ON event_categories
		BEGIN SELECT RAISE(ABORT, 'category write'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	start := time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{Events: []event.Event{{UID: "moved-evt", Title: "E", StartTime: start, EndTime: start.Add(time.Hour), Categories: "work"}}}
	summary := icaltransfer.Import(ctx, a, calA.ID, result)
	if summary.Failed != 1 || !containsSubstring(summary.Warnings, "waits for its move") || containsSubstring(summary.Warnings, "category write") {
		t.Fatalf("import = failed %d, warnings %v; want the pending-move refusal and no category write", summary.Failed, summary.Warnings)
	}
}

// TestImport_SeesATombstoneWrittenBetweenRows imports two events into a
// synced calendar. A trigger writes a move tombstone for the second UID
// while the first row upserts. Each row reads its tombstone in its own
// transaction, so the import must refuse the second event.
func TestImport_SeesATombstoneWrittenBetweenRows(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	calA, calB := syncedPair(t, a)
	if _, err := a.DB.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER late_move AFTER INSERT ON events WHEN NEW.uid = 'first-evt'
		BEGIN INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (%d, 'second-evt', '/cal/a/second-evt.ics', %d); END`, calA.ID, calB.ID)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	start := time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC)
	result := &ical.ImportResult{Events: []event.Event{
		{UID: "first-evt", Title: "First", StartTime: start, EndTime: start.Add(time.Hour)},
		{UID: "second-evt", Title: "Second", StartTime: start, EndTime: start.Add(time.Hour)},
	}}
	summary := icaltransfer.Import(ctx, a, calA.ID, result)
	if summary.Failed != 1 || len(summary.Events) != 1 || !containsSubstring(summary.Warnings, "second-evt waits for its move") {
		t.Fatalf("import = failed %d, events %d, warnings %v; want the second event refused", summary.Failed, len(summary.Events), summary.Warnings)
	}
}
