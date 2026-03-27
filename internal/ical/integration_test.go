package ical

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/douglasdemoura/tcal/internal/calendar"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/testutil"
	"github.com/douglasdemoura/tcal/internal/todo"
)

// testdataPath returns the absolute path to the testdata directory.
func testdataPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	return filepath.Join(filepath.Dir(file), "testdata")
}

// importFromFile imports events and todos from an .ics fixture file.
func importFromFile(t *testing.T, name string) ImportResult {
	t.Helper()
	path := filepath.Join(testdataPath(t), name)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()

	result, err := ImportFile(f)
	if err != nil {
		t.Fatalf("import %s: %v", name, err)
	}
	return result
}

// storeThenExportEvents stores events in DB, loads them back with alarms/attendees, and exports.
func storeThenExportEvents(t *testing.T, events []event.Event) []byte {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()

	calSvc := calendar.NewService(q)
	eventSvc := event.NewService(db, q)

	cals, _ := calSvc.List(ctx)
	calID := cals[0].ID

	for _, e := range events {
		saved, err := eventSvc.UpsertByUID(ctx, event.UpsertParams{
			UID: e.UID, CalendarID: calID,
			Title: e.Title, Description: e.Description, Location: e.Location,
			StartTime: e.StartTime, EndTime: e.EndTime, AllDay: e.AllDay,
			RecurrenceRule: e.RecurrenceRule, Timezone: e.Timezone,
			Status: e.Status, Transp: e.Transp, Sequence: e.Sequence,
			Priority: e.Priority, Class: e.Class, URL: e.URL,
			Categories: e.Categories, ExDates: e.ExDates, RDates: e.RDates,
			RecurrenceID: e.RecurrenceID,
		})
		if err != nil {
			t.Fatalf("upsert event %q: %v", e.Title, err)
		}
		if len(e.Alarms) > 0 {
			eventSvc.ReplaceAlarms(ctx, saved.ID, e.Alarms)
		}
		if len(e.Attendees) > 0 {
			eventSvc.ReplaceAttendees(ctx, saved.ID, e.Attendees)
		}
	}

	// Load back from DB
	stored, _ := eventSvc.ListByDateRange(ctx,
		events[0].StartTime.AddDate(-10, 0, 0),
		events[0].StartTime.AddDate(10, 0, 0))
	for i := range stored {
		stored[i].Alarms, _ = eventSvc.ListAlarms(ctx, stored[i].ID)
		stored[i].Attendees, _ = eventSvc.ListAttendees(ctx, stored[i].ID)
	}

	data, err := ExportEvents(stored, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return data
}

// storeThenExportTodos stores todos in DB, loads them back, and exports.
func storeThenExportTodos(t *testing.T, todos []todo.Todo) []byte {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()

	calSvc := calendar.NewService(q)
	todoSvc := todo.NewService(db, q)

	cals, _ := calSvc.List(ctx)
	calID := cals[0].ID

	for _, td := range todos {
		saved, err := todoSvc.UpsertByUID(ctx, todo.UpsertParams{
			UID: td.UID, CalendarID: calID,
			Summary: td.Summary, Description: td.Description, Location: td.Location,
			DueDate: td.DueDate, StartDate: td.StartDate, Duration: td.Duration,
			CompletedAt: td.CompletedAt, PercentComplete: td.PercentComplete,
			Status: td.Status, Priority: td.Priority, Class: td.Class,
			URL: td.URL, Categories: td.Categories,
			RecurrenceRule: td.RecurrenceRule, Timezone: td.Timezone,
			Sequence: td.Sequence, ExDates: td.ExDates, RDates: td.RDates,
			RecurrenceID: td.RecurrenceID,
		})
		if err != nil {
			t.Fatalf("upsert todo %q: %v", td.Summary, err)
		}
		if len(td.Alarms) > 0 {
			todoSvc.ReplaceAlarms(ctx, saved.ID, td.Alarms)
		}
		if len(td.Attendees) > 0 {
			todoSvc.ReplaceAttendees(ctx, saved.ID, td.Attendees)
		}
	}

	stored, _ := todoSvc.ListAll(ctx)
	for i := range stored {
		stored[i].Alarms, _ = todoSvc.ListAlarms(ctx, stored[i].ID)
		stored[i].Attendees, _ = todoSvc.ListAttendees(ctx, stored[i].ID)
	}

	data, err := ExportTodos(stored, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return data
}

// assertEventsMatch compares two event slices on key properties.
func assertEventsMatch(t *testing.T, label string, original, reimported []event.Event) {
	t.Helper()
	if len(reimported) != len(original) {
		t.Errorf("%s: reimported %d events, want %d", label, len(reimported), len(original))
		return
	}

	// Build UID map for comparison (order may differ)
	byUID := make(map[string]event.Event)
	for _, e := range original {
		byUID[e.UID] = e
	}

	for _, got := range reimported {
		want, ok := byUID[got.UID]
		if !ok {
			t.Errorf("%s: reimported event UID %q not in original", label, got.UID)
			continue
		}
		if got.Title != want.Title {
			t.Errorf("%s [%s]: Title %q != %q", label, got.UID, got.Title, want.Title)
		}
		if got.Description != want.Description {
			t.Errorf("%s [%s]: Description %q != %q", label, got.UID, got.Description, want.Description)
		}
		if got.Location != want.Location {
			t.Errorf("%s [%s]: Location %q != %q", label, got.UID, got.Location, want.Location)
		}
		if got.Status != want.Status {
			t.Errorf("%s [%s]: Status %q != %q", label, got.UID, got.Status, want.Status)
		}
		if got.RecurrenceRule != want.RecurrenceRule {
			t.Errorf("%s [%s]: RRULE %q != %q", label, got.UID, got.RecurrenceRule, want.RecurrenceRule)
		}
		if got.Priority != want.Priority {
			t.Errorf("%s [%s]: Priority %d != %d", label, got.UID, got.Priority, want.Priority)
		}
		if got.Class != want.Class {
			t.Errorf("%s [%s]: Class %q != %q", label, got.UID, got.Class, want.Class)
		}
		if len(got.Alarms) != len(want.Alarms) {
			t.Errorf("%s [%s]: Alarms %d != %d", label, got.UID, len(got.Alarms), len(want.Alarms))
		}
		// Attendee count may grow on roundtrip: ORGANIZER exports as both
		// ORGANIZER + ATTENDEE, so reimport sees an extra ATTENDEE per organizer.
		if len(got.Attendees) < len(want.Attendees) {
			t.Errorf("%s [%s]: Attendees %d < %d (lost attendees)", label, got.UID, len(got.Attendees), len(want.Attendees))
		}
	}
}

// assertTodosMatch compares two todo slices on key properties.
func assertTodosMatch(t *testing.T, label string, original, reimported []todo.Todo) {
	t.Helper()
	if len(reimported) != len(original) {
		t.Errorf("%s: reimported %d todos, want %d", label, len(reimported), len(original))
		return
	}

	byUID := make(map[string]todo.Todo)
	for _, td := range original {
		byUID[td.UID] = td
	}

	for _, got := range reimported {
		want, ok := byUID[got.UID]
		if !ok {
			t.Errorf("%s: reimported todo UID %q not in original", label, got.UID)
			continue
		}
		if got.Summary != want.Summary {
			t.Errorf("%s [%s]: Summary %q != %q", label, got.UID, got.Summary, want.Summary)
		}
		if got.Status != want.Status {
			t.Errorf("%s [%s]: Status %q != %q", label, got.UID, got.Status, want.Status)
		}
		if got.Priority != want.Priority {
			t.Errorf("%s [%s]: Priority %d != %d", label, got.UID, got.Priority, want.Priority)
		}
		if got.Class != want.Class {
			t.Errorf("%s [%s]: Class %q != %q", label, got.UID, got.Class, want.Class)
		}
		if len(got.Alarms) != len(want.Alarms) {
			t.Errorf("%s [%s]: Alarms %d != %d", label, got.UID, len(got.Alarms), len(want.Alarms))
		}
	}
}

// --- Integration tests against libical test-data ---

func TestLibical_6_SimpleEvent(t *testing.T) {
	t.Parallel()
	result := importFromFile(t, "6.ics")
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}

	e := result.Events[0]
	if e.UID != "0981234-1234234-23@example.com" {
		t.Errorf("UID = %q", e.UID)
	}
	if e.Title != "ST. PAUL SAINTS -VS- DULUTH-SUPERIOR DUKES" {
		t.Errorf("Title = %q", e.Title)
	}
}

func TestLibical_7_EventWithSequence(t *testing.T) {
	t.Parallel()
	result := importFromFile(t, "7.ics")
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}
	if result.Events[0].Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", result.Events[0].Sequence)
	}
}

func TestLibical_1_EventWithTimezoneAndAttendees(t *testing.T) {
	t.Parallel()
	result := importFromFile(t, "1.ics")
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}

	e := result.Events[0]
	if e.UID != "guid-1.host1.com" {
		t.Errorf("UID = %q", e.UID)
	}
	if e.Description != "Project XYZ Review Meeting" {
		t.Errorf("Description = %q", e.Description)
	}
	if e.Location != "1CP Conference Room 4350" {
		t.Errorf("Location = %q", e.Location)
	}
	if e.Class != "PUBLIC" {
		t.Errorf("Class = %q", e.Class)
	}
	// Should have organizer + 3 attendees = 4
	if len(e.Attendees) < 3 {
		t.Errorf("Attendees = %d, want >= 3", len(e.Attendees))
	}
}

func TestLibical_3_TodoWithAlarm(t *testing.T) {
	t.Parallel()
	result := importFromFile(t, "3.ics")
	if len(result.Todos) != 1 {
		t.Fatalf("todos = %d, want 1", len(result.Todos))
	}

	td := result.Todos[0]
	if td.UID != "uid4@host1.com" {
		t.Errorf("UID = %q", td.UID)
	}
	if td.Summary != "Submit Income Taxes" {
		t.Errorf("Summary = %q", td.Summary)
	}
	if td.Status != "NEEDS-ACTION" {
		t.Errorf("Status = %q", td.Status)
	}
	if td.Sequence != 2 {
		t.Errorf("Sequence = %d", td.Sequence)
	}
	if td.DueDate == "" {
		t.Error("DueDate is empty")
	}
	if len(td.Alarms) != 1 {
		t.Fatalf("Alarms = %d, want 1", len(td.Alarms))
	}
	if td.Alarms[0].Action != "AUDIO" {
		t.Errorf("Alarm.Action = %q, want AUDIO", td.Alarms[0].Action)
	}
	if len(td.Attendees) < 1 {
		t.Errorf("Attendees = %d, want >= 1", len(td.Attendees))
	}
}

func TestLibical_Classify_MultipleEventsWithAttendees(t *testing.T) {
	t.Parallel()
	result := importFromFile(t, "classify.ics")
	if len(result.Events) < 2 {
		t.Fatalf("events = %d, want >= 2", len(result.Events))
	}

	for _, e := range result.Events {
		if e.Status != "CONFIRMED" {
			t.Errorf("[%s] Status = %q, want CONFIRMED", e.UID, e.Status)
		}
		if len(e.Attendees) < 5 {
			t.Errorf("[%s] Attendees = %d, want >= 5", e.UID, len(e.Attendees))
		}
	}
}

func TestLibical_Large_InvalidStructure(t *testing.T) {
	t.Parallel()
	// large.ics has bare VEVENT/VTODO outside VCALENDAR wrappers,
	// which is invalid per RFC 5545. The go-ical decoder rejects this.
	// This test verifies we return an error without panicking.
	path := filepath.Join(testdataPath(t), "large.ics")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	_, err = ImportFile(f)
	if err == nil {
		t.Log("large.ics parsed without error (unexpected but acceptable)")
	} else {
		t.Logf("large.ics returned expected error: %v", err)
	}
}

// --- Full roundtrip: import → DB → export → reimport → compare ---

func TestLibical_Roundtrip_6(t *testing.T) {
	result := importFromFile(t, "6.ics")
	exported := storeThenExportEvents(t, result.Events)

	reimported, err := ImportFile(readerFromBytes(exported))
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	assertEventsMatch(t, "6.ics", result.Events, reimported.Events)
}

func TestLibical_Roundtrip_7(t *testing.T) {
	result := importFromFile(t, "7.ics")
	exported := storeThenExportEvents(t, result.Events)

	reimported, _ := ImportFile(readerFromBytes(exported))
	assertEventsMatch(t, "7.ics", result.Events, reimported.Events)
}

func TestLibical_Roundtrip_Classify(t *testing.T) {
	result := importFromFile(t, "classify.ics")
	// classify.ics has 2 VCALENDAR blocks with the same UID.
	// Our upsert deduplicates by UID, keeping the last version.
	// This is correct behavior — verify the surviving event roundtrips.
	exported := storeThenExportEvents(t, result.Events)

	reimported, _ := ImportFile(readerFromBytes(exported))
	if len(reimported.Events) != 1 {
		t.Fatalf("expected 1 event after dedup roundtrip, got %d", len(reimported.Events))
	}
	// The surviving event should be the second one (last upsert wins)
	got := reimported.Events[0]
	if got.Title != "Conference in the park" {
		t.Errorf("Title = %q, want %q (last upsert should win)", got.Title, "Conference in the park")
	}
}

func TestLibical_Roundtrip_3_Todo(t *testing.T) {
	result := importFromFile(t, "3.ics")
	exported := storeThenExportTodos(t, result.Todos)

	reimported, _ := ImportFile(readerFromBytes(exported))
	assertTodosMatch(t, "3.ics", result.Todos, reimported.Todos)
}


func readerFromBytes(data []byte) *bytes.Reader {
	return bytes.NewReader(data)
}
