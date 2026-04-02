package ical

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func TestRoundtrip_Event(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:            "roundtrip-event",
		Title:          "Roundtrip Test",
		Description:    "Testing round-trip",
		Location:       "Room C",
		StartTime:      time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 15, 30, 0, 0, time.UTC),
		Status:         "TENTATIVE",
		Transp:         "TRANSPARENT",
		Priority:       7,
		URL:            "https://example.com",
		Categories:     "work",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
		Sequence:       2,
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}

	got := result.Events[0]
	if got.UID != original.UID {
		t.Errorf("UID: %q != %q", got.UID, original.UID)
	}
	if got.Title != original.Title {
		t.Errorf("Title: %q != %q", got.Title, original.Title)
	}
	if got.Description != original.Description {
		t.Errorf("Description: %q != %q", got.Description, original.Description)
	}
	if got.Location != original.Location {
		t.Errorf("Location: %q != %q", got.Location, original.Location)
	}
	if got.Status != original.Status {
		t.Errorf("Status: %q != %q", got.Status, original.Status)
	}
	if got.Transp != original.Transp {
		t.Errorf("Transp: %q != %q", got.Transp, original.Transp)
	}
	if got.Priority != original.Priority {
		t.Errorf("Priority: %d != %d", got.Priority, original.Priority)
	}
	if got.URL != original.URL {
		t.Errorf("URL: %q != %q", got.URL, original.URL)
	}
	if got.RecurrenceRule != original.RecurrenceRule {
		t.Errorf("RRULE: %q != %q", got.RecurrenceRule, original.RecurrenceRule)
	}
	if got.Sequence != original.Sequence {
		t.Errorf("Sequence: %d != %d", got.Sequence, original.Sequence)
	}
	if got.Categories != original.Categories {
		t.Errorf("Categories: %q != %q", got.Categories, original.Categories)
	}
}

func TestRoundtrip_Todo(t *testing.T) {
	t.Parallel()
	// RFC 5545: DUE and DURATION are mutually exclusive in VTODO.
	// Use DUE here; StartDate+Duration tested in TestRoundtrip_TodoStartDuration.
	original := todo.Todo{
		UID:             "roundtrip-todo",
		Summary:         "Roundtrip Todo",
		Description:     "Test todo roundtrip",
		Location:        "Office",
		DueDate:         "2026-04-05",
		StartDate:       "2026-04-01",
		Status:          "IN-PROCESS",
		Priority:        3,
		PercentComplete: 50,
		Class:           "PRIVATE",
		URL:             "https://example.com/task",
		Categories:      "dev",
		Sequence:        1,
		RecurrenceRule:  "FREQ=WEEKLY;COUNT=4",
		ExDates:         "2026-04-08T00:00:00Z",
		RDates:          "2026-05-01T00:00:00Z",
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Reminder"},
		},
		Attachments: []model.Attachment{
			{URI: "https://example.com/doc.pdf", FmtType: "application/pdf"},
		},
		Comments: []string{"First comment", "Second comment"},
		Relations: []model.Relation{
			{RelType: "PARENT", RelUID: "parent-uid-123"},
		},
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}

	got := result.Todos[0]

	// Core fields
	if got.UID != original.UID {
		t.Errorf("UID: %q != %q", got.UID, original.UID)
	}
	if got.Summary != original.Summary {
		t.Errorf("Summary: %q != %q", got.Summary, original.Summary)
	}
	if got.Description != original.Description {
		t.Errorf("Description: %q != %q", got.Description, original.Description)
	}
	if got.Location != original.Location {
		t.Errorf("Location: %q != %q", got.Location, original.Location)
	}

	// Dates
	if got.DueDate != original.DueDate {
		t.Errorf("DueDate: %q != %q", got.DueDate, original.DueDate)
	}
	if got.StartDate != original.StartDate {
		t.Errorf("StartDate: %q != %q", got.StartDate, original.StartDate)
	}

	// Status fields
	if got.Status != original.Status {
		t.Errorf("Status: %q != %q", got.Status, original.Status)
	}
	if got.Priority != original.Priority {
		t.Errorf("Priority: %d != %d", got.Priority, original.Priority)
	}
	if got.PercentComplete != original.PercentComplete {
		t.Errorf("PercentComplete: %d != %d", got.PercentComplete, original.PercentComplete)
	}
	if got.Class != original.Class {
		t.Errorf("Class: %q != %q", got.Class, original.Class)
	}
	if got.URL != original.URL {
		t.Errorf("URL: %q != %q", got.URL, original.URL)
	}
	if got.Categories != original.Categories {
		t.Errorf("Categories: %q != %q", got.Categories, original.Categories)
	}
	if got.Sequence != original.Sequence {
		t.Errorf("Sequence: %d != %d", got.Sequence, original.Sequence)
	}

	// Recurrence
	if got.RecurrenceRule != original.RecurrenceRule {
		t.Errorf("RecurrenceRule: %q != %q", got.RecurrenceRule, original.RecurrenceRule)
	}
	if got.ExDates == "" {
		t.Error("ExDates lost on round-trip")
	}
	if got.RDates == "" {
		t.Error("RDates lost on round-trip")
	}

	// Alarms
	if len(got.Alarms) != 1 {
		t.Errorf("Alarms: got %d, want 1", len(got.Alarms))
	} else {
		if got.Alarms[0].Action != "DISPLAY" {
			t.Errorf("Alarm action: %q != %q", got.Alarms[0].Action, "DISPLAY")
		}
		if got.Alarms[0].TriggerValue != "-PT15M" {
			t.Errorf("Alarm trigger: %q != %q", got.Alarms[0].TriggerValue, "-PT15M")
		}
	}

	// Attachments
	if len(got.Attachments) != 1 {
		t.Errorf("Attachments: got %d, want 1", len(got.Attachments))
	} else if got.Attachments[0].URI != original.Attachments[0].URI {
		t.Errorf("Attachment URI: %q != %q", got.Attachments[0].URI, original.Attachments[0].URI)
	}

	// Comments
	if len(got.Comments) != 2 {
		t.Errorf("Comments: got %d, want 2", len(got.Comments))
	}

	// Relations
	if len(got.Relations) != 1 {
		t.Errorf("Relations: got %d, want 1", len(got.Relations))
	} else {
		if got.Relations[0].RelType != "PARENT" {
			t.Errorf("Relation type: %q != %q", got.Relations[0].RelType, "PARENT")
		}
		if got.Relations[0].RelUID != "parent-uid-123" {
			t.Errorf("Relation UID: %q != %q", got.Relations[0].RelUID, "parent-uid-123")
		}
	}
}

func TestRoundtrip_TodoDateOnlyDue(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "roundtrip-todo-dateonly",
		Summary:   "Date Only Due",
		DueDate:   "2026-04-01",
		Status:    "NEEDS-ACTION",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Verify the raw iCal contains VALUE=DATE
	ics := string(data)
	if !strings.Contains(ics, "DUE;VALUE=DATE:20260401") {
		t.Errorf("expected DUE;VALUE=DATE:20260401 in export, got:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]
	if got.DueDate != "2026-04-01" {
		t.Errorf("DueDate round-trip: got %q, want %q", got.DueDate, "2026-04-01")
	}
}

func TestRoundtrip_TodoWithCompletedAt(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:             "roundtrip-todo-completed",
		Summary:         "Completed Todo",
		Status:          "COMPLETED",
		CompletedAt:     "2026-04-01T10:00:00Z",
		PercentComplete: 100,
		CreatedAt:       time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]
	if got.Status != "COMPLETED" {
		t.Errorf("Status: %q != COMPLETED", got.Status)
	}
	if got.CompletedAt == "" {
		t.Error("CompletedAt lost on round-trip")
	}
	if got.PercentComplete != 100 {
		t.Errorf("PercentComplete: %d != 100", got.PercentComplete)
	}
}

func TestRoundtrip_TodoWithAttendees(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:     "roundtrip-todo-attendees",
		Summary: "Todo with attendees",
		Status:  "NEEDS-ACTION",
		Attendees: []model.Attendee{
			{Email: "org@test.com", Name: "Org", RSVPStatus: "ACCEPTED", Role: "CHAIR", Organizer: true},
			{Email: "user@test.com", Name: "User", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "ORGANIZER") {
		t.Fatal("ICS missing ORGANIZER property")
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]

	if len(got.Attendees) != 2 {
		t.Fatalf("Attendees: %d, want 2", len(got.Attendees))
	}

	var foundOrganizer bool
	for _, a := range got.Attendees {
		if a.Organizer {
			foundOrganizer = true
			if a.Email != "org@test.com" {
				t.Errorf("Organizer email: %q, want org@test.com", a.Email)
			}
		}
	}
	if !foundOrganizer {
		t.Error("No attendee has Organizer=true after roundtrip")
	}
}

func TestRoundtrip_TodoWithTimezone(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "roundtrip-todo-tz",
		Summary:   "Timezone Todo",
		DueDate:   "2026-04-05T21:00:00Z",
		StartDate: "2026-04-01T13:00:00Z",
		Timezone:  "America/New_York",
		Status:    "NEEDS-ACTION",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Error("ICS missing VTIMEZONE component")
	}
	if !strings.Contains(ics, "TZID:America/New_York") {
		t.Error("ICS missing TZID:America/New_York")
	}
	if !strings.Contains(ics, "TZID=America/New_York") {
		t.Errorf("ICS missing TZID parameter on DUE or DTSTART:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]
	if got.Timezone != original.Timezone {
		t.Errorf("Timezone: %q != %q", got.Timezone, original.Timezone)
	}
}

func TestRoundtrip_TodoWithGeo(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "roundtrip-todo-geo",
		Summary:   "Geo Todo",
		Status:    "NEEDS-ACTION",
		Geo:       "37.386013;-122.082932",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "GEO:37.386013;-122.082932") {
		t.Errorf("ICS missing GEO property:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]
	if got.Geo != original.Geo {
		t.Errorf("Geo: %q != %q", got.Geo, original.Geo)
	}
}

func TestRoundtrip_TodoStartDuration(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "roundtrip-todo-duration",
		Summary:   "Duration Todo",
		StartDate: "2026-04-01",
		Duration:  "PT4H",
		Status:    "NEEDS-ACTION",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]
	if got.StartDate != "2026-04-01" {
		t.Errorf("StartDate: %q != %q", got.StartDate, "2026-04-01")
	}
	if got.Duration != "PT4H" {
		t.Errorf("Duration: %q != %q", got.Duration, "PT4H")
	}
}

func TestRoundtrip_EventWithAlarmsAttendees(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-alarm",
		Title:     "Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Soon"},
		},
		Attendees: []model.Attendee{
			{Email: "org@test.com", Name: "Org", RSVPStatus: "ACCEPTED", Role: "CHAIR", Organizer: true},
			{Email: "user@test.com", Name: "User", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
		},
	}

	data, _ := ExportEvents([]event.Event{original}, "")
	result, _ := ImportFile(strings.NewReader(string(data)))

	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if len(got.Alarms) != 1 {
		t.Errorf("Alarms: %d, want 1", len(got.Alarms))
	} else if got.Alarms[0].TriggerValue != "-PT15M" {
		t.Errorf("Alarm trigger: %q", got.Alarms[0].TriggerValue)
	}

	if len(got.Attendees) != 2 {
		t.Errorf("Attendees: %d, want 2", len(got.Attendees))
	}
}

func TestRoundtrip_AlarmUID(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-alarm-uid",
		Title:     "Alarm UID Test",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{UID: "alarm-uid-abc123", Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Reminder"},
			{UID: "alarm-uid-def456", Action: "EMAIL", TriggerValue: "-PT1H", Summary: "Heads up", Description: "Meeting soon"},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events, want 1", len(result.Events))
	}
	got := result.Events[0]
	if len(got.Alarms) != 2 {
		t.Fatalf("got %d alarms, want 2", len(got.Alarms))
	}
	if got.Alarms[0].UID != "alarm-uid-abc123" {
		t.Errorf("alarm[0] UID = %q, want %q", got.Alarms[0].UID, "alarm-uid-abc123")
	}
	if got.Alarms[1].UID != "alarm-uid-def456" {
		t.Errorf("alarm[1] UID = %q, want %q", got.Alarms[1].UID, "alarm-uid-def456")
	}
}

func TestRoundtrip_OrganizerNotDuplicated(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-org-dedup",
		Title:     "Organizer Dedup",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Attendees: []model.Attendee{
			{Email: "org@test.com", Name: "Organizer", RSVPStatus: "ACCEPTED", Role: "CHAIR", Organizer: true},
			{Email: "attendee@test.com", Name: "Attendee", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
		},
	}

	// Export emits ORGANIZER + ATTENDEE for the organizer.
	// Import must not create a duplicate entry for the organizer email.
	data, _ := ExportEvents([]event.Event{original}, "")
	ics := string(data)

	// Verify both ORGANIZER and ATTENDEE appear in the ICS
	if !strings.Contains(ics, "ORGANIZER") {
		t.Fatal("ICS missing ORGANIZER property")
	}

	result, _ := ImportFile(strings.NewReader(ics))
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if len(got.Attendees) != 2 {
		t.Fatalf("Attendees: %d, want 2 (organizer should not be duplicated)", len(got.Attendees))
	}

	// Verify the organizer flag survived
	var foundOrganizer bool
	for _, a := range got.Attendees {
		if a.Organizer {
			foundOrganizer = true
			if a.Email != "org@test.com" {
				t.Errorf("Organizer email: %q, want org@test.com", a.Email)
			}
		}
	}
	if !foundOrganizer {
		t.Error("No attendee has Organizer=true after roundtrip")
	}
}

func TestRoundtrip_AlarmRepeatDurationRelated(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-alarm-repeat",
		Title:     "Repeat Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT10M",
				Description:  "Before start",
				Repeat:       3,
				Duration:     "PT5M",
				Related:      "START",
			},
			{
				Action:       "EMAIL",
				TriggerValue: "PT0S",
				Description:  "At the end",
				Repeat:       1,
				Duration:     "PT15M",
				Related:      "END",
				Attendees: []model.AlarmAttendee{
					{Email: "alice@test.com", Name: "Alice"},
					{Email: "bob@test.com", Name: "Bob"},
				},
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if len(got.Alarms) != 2 {
		t.Fatalf("Alarms count: %d, want 2", len(got.Alarms))
	}

	a0 := got.Alarms[0]
	if a0.TriggerValue != "-PT10M" {
		t.Errorf("Alarm[0] trigger: %q", a0.TriggerValue)
	}
	if a0.Repeat != 3 {
		t.Errorf("Alarm[0] repeat: %d, want 3", a0.Repeat)
	}
	if a0.Duration != "PT5M" {
		t.Errorf("Alarm[0] duration: %q, want PT5M", a0.Duration)
	}
	if a0.Related != "START" {
		t.Errorf("Alarm[0] related: %q, want START", a0.Related)
	}

	a1 := got.Alarms[1]
	if a1.Related != "END" {
		t.Errorf("Alarm[1] related: %q, want END", a1.Related)
	}
	if a1.Repeat != 1 {
		t.Errorf("Alarm[1] repeat: %d, want 1", a1.Repeat)
	}
	if len(a1.Attendees) != 2 {
		t.Fatalf("Alarm[1] attendees: %d, want 2", len(a1.Attendees))
	}
	if a1.Attendees[0].Email != "alice@test.com" {
		t.Errorf("Alarm[1] attendee[0] email: %q", a1.Attendees[0].Email)
	}
	if a1.Attendees[1].Name != "Bob" {
		t.Errorf("Alarm[1] attendee[1] name: %q", a1.Attendees[1].Name)
	}
}

func TestRoundtrip_BlobAttachment(t *testing.T) {
	t.Parallel()
	blobData := []byte("Hello, this is a test PDF content")
	original := event.Event{
		UID:       "roundtrip-blob-attach",
		Title:     "Blob Attachment Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Attachments: []model.Attachment{
			{URI: "https://example.com/doc.pdf", FmtType: "application/pdf"},
			{Data: blobData, FmtType: "application/pdf", Filename: "slides.pdf"},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if len(got.Attachments) != 2 {
		t.Fatalf("Attachments count: %d, want 2", len(got.Attachments))
	}

	// URI attachment
	uri := got.Attachments[0]
	if uri.URI != "https://example.com/doc.pdf" {
		t.Errorf("URI attachment URI: %q", uri.URI)
	}
	if uri.FmtType != "application/pdf" {
		t.Errorf("URI attachment FmtType: %q", uri.FmtType)
	}
	if uri.Data != nil {
		t.Errorf("URI attachment should have nil Data")
	}

	// Blob attachment
	blob := got.Attachments[1]
	if blob.URI != "" {
		t.Errorf("Blob attachment should have empty URI, got %q", blob.URI)
	}
	if !bytes.Equal(blob.Data, blobData) {
		t.Errorf("Blob attachment data mismatch: got %d bytes, want %d", len(blob.Data), len(blobData))
	}
	if blob.FmtType != "application/pdf" {
		t.Errorf("Blob attachment FmtType: %q", blob.FmtType)
	}
	if blob.Filename != "slides.pdf" {
		t.Errorf("Blob attachment Filename: %q, want slides.pdf", blob.Filename)
	}
}

func TestRoundtrip_TodoAlarmRepeat(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "roundtrip-todo-alarm-repeat",
		Summary:   "Todo with alarm repeat",
		Status:    "NEEDS-ACTION",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT30M",
				Description:  "Reminder",
				Repeat:       2,
				Duration:     "PT10M",
				Related:      "END",
			},
		},
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]

	if len(got.Alarms) != 1 {
		t.Fatalf("Alarms count: %d, want 1", len(got.Alarms))
	}
	a := got.Alarms[0]
	if a.Repeat != 2 {
		t.Errorf("Repeat: %d, want 2", a.Repeat)
	}
	if a.Duration != "PT10M" {
		t.Errorf("Duration: %q, want PT10M", a.Duration)
	}
	if a.Related != "END" {
		t.Errorf("Related: %q, want END", a.Related)
	}
}

func TestRoundtrip_AllDayEventDateStable(t *testing.T) {
	t.Parallel()
	// All-day event dates must survive export→import without shifting.
	// Regression: previously, midnight-local became midnight-UTC on import,
	// causing date drift for non-UTC timezones.
	original := event.Event{
		UID:       "roundtrip-allday-stable",
		Title:     "All Day Stable",
		StartTime: time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 16, 0, 0, 0, 0, time.Local),
		AllDay:    true,
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if !got.AllDay {
		t.Error("AllDay flag lost on round-trip")
	}
	// The local date must be preserved: April 15 → April 15
	if got.StartTime.Year() != 2026 || got.StartTime.Month() != 4 || got.StartTime.Day() != 15 {
		t.Errorf("StartTime date shifted: got %s, want 2026-04-15", got.StartTime.Format("2006-01-02"))
	}
	if got.EndTime.Year() != 2026 || got.EndTime.Month() != 4 || got.EndTime.Day() != 16 {
		t.Errorf("EndTime date shifted: got %s, want 2026-04-16", got.EndTime.Format("2006-01-02"))
	}
}

func TestRoundtrip_MultipleExdatesRdates(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-multi-exdate",
		Title:     "Multi ExDate Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		ExDates:   "2026-04-08T14:00:00Z,2026-04-15T14:00:00Z,2026-04-22T14:00:00Z",
		RDates:    "2026-05-01T14:00:00Z,2026-05-08T14:00:00Z",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	// Count EXDATE entries
	exdateCount := len(strings.Split(got.ExDates, ","))
	if exdateCount != 3 {
		t.Errorf("ExDates: got %d entries (%q), want 3", exdateCount, got.ExDates)
	}

	rdateCount := len(strings.Split(got.RDates, ","))
	if rdateCount != 2 {
		t.Errorf("RDates: got %d entries (%q), want 2", rdateCount, got.RDates)
	}
}

func TestRoundtrip_MultipleComments(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-multi-comment",
		Title:     "Multi Comment Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Comments:  []string{"First comment", "Second comment", "Third comment"},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if len(got.Comments) != 3 {
		t.Fatalf("Comments count: %d, want 3", len(got.Comments))
	}
	for i, want := range original.Comments {
		if got.Comments[i] != want {
			t.Errorf("Comment[%d]: %q, want %q", i, got.Comments[i], want)
		}
	}
}

func TestRoundtrip_EventWithGeo(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-geo",
		Title:     "Geo Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Geo:       "37.386013;-122.082932",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Verify GEO appears in the ICS output
	ics := string(data)
	if !strings.Contains(ics, "GEO:37.386013;-122.082932") {
		t.Errorf("ICS missing GEO property:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}

	got := result.Events[0]
	if got.Geo != original.Geo {
		t.Errorf("Geo: %q != %q", got.Geo, original.Geo)
	}
}

func TestRoundtrip_EventWithContacts(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-contacts",
		Title:     "Contacts Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Contacts:  []string{"John Smith, 555-1234", "Support: support@example.com"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if len(got.Contacts) != 2 {
		t.Fatalf("Contacts count: %d, want 2 (got %v)", len(got.Contacts), got.Contacts)
	}
	for i, want := range original.Contacts {
		if got.Contacts[i] != want {
			t.Errorf("Contact[%d]: %q, want %q", i, got.Contacts[i], want)
		}
	}
}

func TestRoundtrip_EventWithRelations(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-relations",
		Title:     "Relations Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Relations: []model.Relation{
			{RelType: "PARENT", RelUID: "parent-uid-123"},
			{RelType: "CHILD", RelUID: "child-uid-456"},
			{RelType: "SIBLING", RelUID: "sibling-uid-789"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	// PARENT is default RELTYPE, so it should be omitted in export
	if !strings.Contains(ics, "RELATED-TO:parent-uid-123") {
		t.Errorf("ICS missing RELATED-TO for PARENT:\n%s", ics)
	}
	if !strings.Contains(ics, "RELATED-TO;RELTYPE=CHILD:child-uid-456") {
		t.Errorf("ICS missing RELATED-TO;RELTYPE=CHILD:\n%s", ics)
	}
	if !strings.Contains(ics, "RELATED-TO;RELTYPE=SIBLING:sibling-uid-789") {
		t.Errorf("ICS missing RELATED-TO;RELTYPE=SIBLING:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if len(got.Relations) != 3 {
		t.Fatalf("Relations count: %d, want 3 (got %v)", len(got.Relations), got.Relations)
	}
	for i, want := range original.Relations {
		if got.Relations[i].RelType != want.RelType {
			t.Errorf("Relation[%d] RelType: %q, want %q", i, got.Relations[i].RelType, want.RelType)
		}
		if got.Relations[i].RelUID != want.RelUID {
			t.Errorf("Relation[%d] RelUID: %q, want %q", i, got.Relations[i].RelUID, want.RelUID)
		}
	}
}

func TestRoundtrip_EventWithResources(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-resources",
		Title:     "Resources Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Resources: []string{"PROJECTOR", "WHITEBOARD", "EASEL"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	// Verify RESOURCES appears with unescaped commas (list separator, not text)
	if !strings.Contains(ics, "RESOURCES:PROJECTOR,WHITEBOARD,EASEL") {
		t.Errorf("ICS RESOURCES not formatted as comma-separated list:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}
	got := result.Events[0]

	if len(got.Resources) != 3 {
		t.Fatalf("Resources count: %d, want 3 (got %v)", len(got.Resources), got.Resources)
	}
	for i, want := range original.Resources {
		if got.Resources[i] != want {
			t.Errorf("Resource[%d]: %q, want %q", i, got.Resources[i], want)
		}
	}
}

func TestRoundtrip_TodoWithContacts(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "roundtrip-todo-contacts",
		Summary:   "Contacts Todo",
		Status:    "NEEDS-ACTION",
		Contacts:  []string{"John Smith, 555-1234", "Support: support@example.com"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]

	if len(got.Contacts) != 2 {
		t.Fatalf("Contacts count: %d, want 2 (got %v)", len(got.Contacts), got.Contacts)
	}
	for i, want := range original.Contacts {
		if got.Contacts[i] != want {
			t.Errorf("Contact[%d]: %q, want %q", i, got.Contacts[i], want)
		}
	}
}

func TestRoundtrip_TodoWithResources(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "roundtrip-todo-resources",
		Summary:   "Resources Todo",
		Status:    "NEEDS-ACTION",
		Resources: []string{"LAPTOP", "MONITOR"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "RESOURCES:LAPTOP,MONITOR") {
		t.Errorf("ICS RESOURCES not formatted as comma-separated list:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}
	got := result.Todos[0]

	if len(got.Resources) != 2 {
		t.Fatalf("Resources count: %d, want 2 (got %v)", len(got.Resources), got.Resources)
	}
	for i, want := range original.Resources {
		if got.Resources[i] != want {
			t.Errorf("Resource[%d]: %q, want %q", i, got.Resources[i], want)
		}
	}
}

func TestRoundtrip_EventWithTimezone(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	original := event.Event{
		UID:       "roundtrip-tz",
		Title:     "Timezone Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, loc),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, loc),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	// Verify VTIMEZONE and TZID appear in the ICS output
	if !strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Error("ICS missing VTIMEZONE component")
	}
	if !strings.Contains(ics, "TZID:America/New_York") {
		t.Error("ICS missing TZID:America/New_York")
	}
	if !strings.Contains(ics, "DTSTART;TZID=America/New_York:20260401T140000") {
		t.Errorf("ICS missing DTSTART with TZID:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events", len(result.Events))
	}

	got := result.Events[0]
	if got.Timezone != original.Timezone {
		t.Errorf("Timezone: %q != %q", got.Timezone, original.Timezone)
	}
	// Verify the time itself is preserved (2pm EDT = 18:00 UTC)
	if got.StartTime.UTC().Hour() != 18 {
		t.Errorf("StartTime UTC hour: %d, want 18 (2pm EDT)", got.StartTime.UTC().Hour())
	}
}

func TestRoundtrip_TodoDateOnlyExdateRdate(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:            "roundtrip-todo-dateonly-exdate",
		Summary:        "Date-only EXDATE/RDATE",
		DueDate:        "2026-04-15",
		StartDate:      "2026-04-01",
		Status:         "IN-PROCESS",
		RecurrenceRule: "FREQ=WEEKLY;COUNT=8",
		ExDates:        "2026-04-08",
		RDates:         "2026-05-01",
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	// Verify date-only EXDATE exports as VALUE=DATE
	if !strings.Contains(ics, "EXDATE;VALUE=DATE:20260408") {
		t.Errorf("expected EXDATE;VALUE=DATE:20260408, got:\n%s", ics)
	}
	// Verify date-only RDATE exports as VALUE=DATE
	if !strings.Contains(ics, "RDATE;VALUE=DATE:20260501") {
		t.Errorf("expected RDATE;VALUE=DATE:20260501, got:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos", len(result.Todos))
	}

	got := result.Todos[0]
	// Verify date-only format is preserved through roundtrip
	if got.ExDates != "2026-04-08" {
		t.Errorf("ExDates roundtrip: %q, want %q", got.ExDates, "2026-04-08")
	}
	if got.RDates != "2026-05-01" {
		t.Errorf("RDates roundtrip: %q, want %q", got.RDates, "2026-05-01")
	}
}

func TestRoundtrip_AlarmSummary_EMAIL(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-alarm-summary-email",
		Title:     "Email Summary Test",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{
				Action:       "EMAIL",
				TriggerValue: "-PT1H",
				Description:  "Email reminder",
				Summary:      "Custom Subject Line",
				Related:      "START",
				Attendees:    []model.AlarmAttendee{{Email: "test@example.com"}},
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "SUMMARY:Custom Subject Line") {
		t.Errorf("exported ICS missing SUMMARY; got:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 1 {
		t.Fatalf("reimport: events=%d", len(result.Events))
	}
	got := result.Events[0].Alarms[0]
	if got.Summary != "Custom Subject Line" {
		t.Errorf("Summary roundtrip: %q, want %q", got.Summary, "Custom Subject Line")
	}
}

func TestRoundtrip_AlarmSummary_DISPLAY(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-alarm-summary-display",
		Title:     "Display Summary Test",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT15M",
				Description:  "Reminder",
				Summary:      "Display Note",
				Related:      "START",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "SUMMARY:Display Note") {
		t.Errorf("exported ICS missing SUMMARY for DISPLAY alarm; got:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	got := result.Events[0].Alarms[0]
	if got.Summary != "Display Note" {
		t.Errorf("Summary roundtrip: %q, want %q", got.Summary, "Display Note")
	}
}

func TestRoundtrip_AlarmSummary_EMAIL_Fallback(t *testing.T) {
	t.Parallel()
	// EMAIL alarm with no Summary should get the event title as SUMMARY on export
	original := event.Event{
		UID:       "roundtrip-alarm-summary-fallback",
		Title:     "Team Meeting",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{
				Action:       "EMAIL",
				TriggerValue: "-PT1H",
				Description:  "Reminder",
				Related:      "START",
				Attendees:    []model.AlarmAttendee{{Email: "test@example.com"}},
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "SUMMARY:Team Meeting") {
		t.Errorf("exported ICS missing fallback SUMMARY; got:\n%s", ics)
	}
}

func TestRoundtrip_AlarmSummary_SpecialChars(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "roundtrip-alarm-summary-special",
		Title:     "Special Chars Test",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{
				Action:       "EMAIL",
				TriggerValue: "-PT1H",
				Description:  "Reminder",
				Summary:      "Meeting: Q1 Review",
				Related:      "START",
				Attendees:    []model.AlarmAttendee{{Email: "test@example.com"}},
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	got := result.Events[0].Alarms[0]
	if got.Summary != "Meeting: Q1 Review" {
		t.Errorf("Summary with special chars: %q, want %q", got.Summary, "Meeting: Q1 Review")
	}
}

func TestRoundtrip_AlarmAcknowledged(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "ack-roundtrip",
		Title:     "Acknowledged Alarm",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT15M",
				Description:  "Reminder",
				Related:      "START",
				Acknowledged: "20260401T140000Z",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "ACKNOWLEDGED:20260401T140000Z") {
		t.Fatal("exported ICS missing ACKNOWLEDGED property")
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) == 0 || len(result.Events[0].Alarms) == 0 {
		t.Fatal("no alarms after round-trip")
	}
	got := result.Events[0].Alarms[0]
	if got.Acknowledged != "20260401T140000Z" {
		t.Errorf("Acknowledged = %q, want %q", got.Acknowledged, "20260401T140000Z")
	}
}

func TestRoundtrip_TodoAlarmAcknowledged(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "todo-ack-roundtrip",
		Summary:   "Acknowledged Todo Alarm",
		Status:    "NEEDS-ACTION",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT30M",
				Description:  "Todo reminder",
				Related:      "START",
				Acknowledged: "20260401T090000Z",
			},
		},
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "ACKNOWLEDGED:20260401T090000Z") {
		t.Fatal("exported ICS missing ACKNOWLEDGED property")
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) == 0 || len(result.Todos[0].Alarms) == 0 {
		t.Fatal("no alarms after round-trip")
	}
	got := result.Todos[0].Alarms[0]
	if got.Acknowledged != "20260401T090000Z" {
		t.Errorf("Acknowledged = %q, want %q", got.Acknowledged, "20260401T090000Z")
	}
}

func TestExport_ProductID(t *testing.T) {
	original := ProductID
	defer func() { ProductID = original }()

	ProductID = "-//Custom//Product//EN"

	events := []event.Event{{
		UID:       "prodid-test",
		Title:     "PRODID Test",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(string(data), "-//Custom//Product//EN") {
		t.Error("exported ICS does not contain custom PRODID")
	}
}

func TestRoundtrip_TodoDueTZID(t *testing.T) {
	t.Parallel()
	// A todo with DUE;TZID but no DTSTART — timezone should be extracted from DUE.
	original := todo.Todo{
		UID:       "roundtrip-todo-due-tzid",
		Summary:   "Todo with DUE timezone",
		DueDate:   "2026-06-15T17:00:00Z",
		Status:    "NEEDS-ACTION",
		Timezone:  "America/Chicago",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos, want 1", len(result.Todos))
	}

	got := result.Todos[0]
	if got.Timezone != "America/Chicago" {
		t.Errorf("Timezone: got %q, want %q", got.Timezone, "America/Chicago")
	}
}

func TestRoundtrip_AllDayEventEXDATE(t *testing.T) {
	t.Parallel()
	// All-day recurring event with date-only EXDATE should roundtrip as date-only.
	original := event.Event{
		UID:            "roundtrip-allday-exdate",
		Title:          "Weekly All-Day",
		StartTime:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local),
		EndTime:        time.Date(2026, 4, 2, 0, 0, 0, 0, time.Local),
		AllDay:         true,
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
		ExDates:        "2026-04-08",
		RDates:         "2026-05-01",
		Status:         "CONFIRMED",
		Transp:         "OPAQUE",
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Verify the exported ICS contains VALUE=DATE for EXDATE
	ics := string(data)
	if !strings.Contains(ics, "VALUE=DATE") {
		t.Errorf("exported EXDATE missing VALUE=DATE:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events, want 1", len(result.Events))
	}

	got := result.Events[0]
	if got.ExDates != "2026-04-08" {
		t.Errorf("ExDates roundtrip: got %q, want %q", got.ExDates, "2026-04-08")
	}
	if got.RDates != "2026-05-01" {
		t.Errorf("RDates roundtrip: got %q, want %q", got.RDates, "2026-05-01")
	}
}

func TestImportFile_Warnings(t *testing.T) {
	t.Parallel()
	// A VEVENT missing UID should produce a warning, not be silently dropped.
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		"BEGIN:VEVENT\r\nSUMMARY:Good Event\r\nUID:good-uid\r\n" +
		"DTSTART:20260401T140000Z\r\nDTEND:20260401T150000Z\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nSUMMARY:Bad Event No UID\r\n" +
		"DTSTART:20260402T140000Z\r\nDTEND:20260402T150000Z\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Errorf("Events: got %d, want 1", len(result.Events))
	}
	if len(result.Warnings) != 1 {
		t.Errorf("Warnings: got %d, want 1", len(result.Warnings))
	}
	if len(result.Warnings) > 0 && !strings.Contains(result.Warnings[0], "missing UID") {
		t.Errorf("Warning: got %q, want something about missing UID", result.Warnings[0])
	}
}

func TestRoundtrip_AlarmAttachURI(t *testing.T) {
	t.Parallel()
	// AUDIO alarm with ATTACH URI + FMTTYPE should roundtrip.
	original := event.Event{
		UID:       "roundtrip-alarm-attach",
		Title:     "Event with Audio Alarm",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Alarms: []model.Alarm{
			{
				Action:        "AUDIO",
				TriggerValue:  "-PT15M",
				Description:   "Alarm",
				AttachURI:     "http://example.com/sounds/bell.aud",
				AttachFmtType: "audio/basic",
			},
		},
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Verify ATTACH is in the output
	ics := string(data)
	if !strings.Contains(ics, "ATTACH") {
		t.Errorf("exported ICS missing ATTACH:\n%s", ics)
	}
	if !strings.Contains(ics, "FMTTYPE=audio/basic") {
		t.Errorf("exported ICS missing FMTTYPE:\n%s", ics)
	}

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events, want 1", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("Alarms: got %d, want 1", len(got.Alarms))
	}
	alarm := got.Alarms[0]
	if alarm.AttachURI != "http://example.com/sounds/bell.aud" {
		t.Errorf("AttachURI: got %q, want %q", alarm.AttachURI, "http://example.com/sounds/bell.aud")
	}
	if alarm.AttachFmtType != "audio/basic" {
		t.Errorf("AttachFmtType: got %q, want %q", alarm.AttachFmtType, "audio/basic")
	}
}

func TestRoundtrip_EventDuration(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:           "duration-event",
		Title:         "Duration Test",
		StartTime:     time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		DurationValue: "PT1H",
		Status:        "CONFIRMED",
		Transp:        "OPAQUE",
		CreatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	exported := string(data)

	// DURATION must be present, DTEND must NOT be present (RFC 5545 mutual exclusivity).
	if !strings.Contains(exported, "DURATION:PT1H") {
		t.Errorf("exported ICS missing DURATION:PT1H\n%s", exported)
	}
	if strings.Contains(exported, "DTEND") {
		t.Errorf("exported ICS contains DTEND when DURATION is set\n%s", exported)
	}

	result, err := ImportFile(strings.NewReader(exported))
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events, want 1", len(result.Events))
	}

	got := result.Events[0]
	if got.DurationValue != "PT1H" {
		t.Errorf("DurationValue: got %q, want %q", got.DurationValue, "PT1H")
	}
	if got.EndTime.Sub(got.StartTime) != time.Hour {
		t.Errorf("EndTime-StartTime: got %v, want 1h", got.EndTime.Sub(got.StartTime))
	}
}

func TestRoundtrip_EventDurationFloating(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:           "duration-floating",
		Title:         "Floating Duration",
		StartTime:     time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC),
		DurationValue: "PT30M",
		Timezone:      "FLOATING",
		Status:        "CONFIRMED",
		Transp:        "OPAQUE",
		CreatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	exported := string(data)
	if !strings.Contains(exported, "DURATION:PT30M") {
		t.Errorf("exported ICS missing DURATION:PT30M\n%s", exported)
	}
	if strings.Contains(exported, "DTEND") {
		t.Errorf("exported ICS contains DTEND when DURATION is set\n%s", exported)
	}
}

func TestRoundtrip_EventDurationWithTimezone(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:           "duration-tz",
		Title:         "TZ Duration",
		StartTime:     time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC),
		DurationValue: "PT2H",
		Timezone:      "America/New_York",
		Status:        "CONFIRMED",
		Transp:        "OPAQUE",
		CreatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	exported := string(data)
	if !strings.Contains(exported, "DURATION:PT2H") {
		t.Errorf("exported ICS missing DURATION:PT2H\n%s", exported)
	}
	if strings.Contains(exported, "DTEND") {
		t.Errorf("exported ICS contains DTEND when DURATION is set\n%s", exported)
	}
}

func TestRoundtrip_EventNoDuration(t *testing.T) {
	// When DurationValue is empty, export should emit DTEND, not DURATION.
	t.Parallel()
	original := event.Event{
		UID:       "no-duration-event",
		Title:     "No Duration",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	exported := string(data)
	if !strings.Contains(exported, "DTEND") {
		t.Errorf("exported ICS missing DTEND when DurationValue is empty\n%s", exported)
	}
	if strings.Contains(exported, "DURATION:") {
		t.Errorf("exported ICS contains DURATION when DurationValue is empty\n%s", exported)
	}
}

func TestRoundtrip_EventDtStamp(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:       "dtstamp-event",
		Title:     "DtStamp Test",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		DtStamp:   "2026-03-25T08:00:00Z",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	exported := string(data)

	// DTSTAMP should reflect DtStamp, not UpdatedAt.
	if !strings.Contains(exported, "DTSTAMP:20260325T080000Z") {
		t.Errorf("exported DTSTAMP should be 20260325T080000Z, got:\n%s", exported)
	}
	// LAST-MODIFIED should still use UpdatedAt.
	if !strings.Contains(exported, "LAST-MODIFIED:20260328T120000Z") {
		t.Errorf("exported LAST-MODIFIED should be 20260328T120000Z, got:\n%s", exported)
	}

	result, err := ImportFile(strings.NewReader(exported))
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("reimported %d events, want 1", len(result.Events))
	}

	got := result.Events[0]
	if got.DtStamp != "2026-03-25T08:00:00Z" {
		t.Errorf("DtStamp: got %q, want %q", got.DtStamp, "2026-03-25T08:00:00Z")
	}
}

func TestRoundtrip_EventDtStampEmpty(t *testing.T) {
	// When DtStamp is empty, DTSTAMP should fall back to UpdatedAt.
	t.Parallel()
	original := event.Event{
		UID:       "dtstamp-empty",
		Title:     "Empty DtStamp",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	exported := string(data)
	// DTSTAMP should fall back to UpdatedAt.
	if !strings.Contains(exported, "DTSTAMP:20260328T120000Z") {
		t.Errorf("exported DTSTAMP should fall back to UpdatedAt 20260328T120000Z, got:\n%s", exported)
	}
}

func TestRoundtrip_TodoDtStamp(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:       "dtstamp-todo",
		Summary:   "DtStamp Todo Test",
		DueDate:   "2026-04-15",
		DtStamp:   "2026-03-20T10:00:00Z",
		Status:    "NEEDS-ACTION",
		CreatedAt: time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 22, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	exported := string(data)
	if !strings.Contains(exported, "DTSTAMP:20260320T100000Z") {
		t.Errorf("exported DTSTAMP should be 20260320T100000Z, got:\n%s", exported)
	}
	if !strings.Contains(exported, "LAST-MODIFIED:20260322T120000Z") {
		t.Errorf("exported LAST-MODIFIED should be 20260322T120000Z, got:\n%s", exported)
	}

	result, err := ImportFile(strings.NewReader(exported))
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("reimported %d todos, want 1", len(result.Todos))
	}

	got := result.Todos[0]
	if got.DtStamp != "2026-03-20T10:00:00Z" {
		t.Errorf("DtStamp: got %q, want %q", got.DtStamp, "2026-03-20T10:00:00Z")
	}
}

func TestRoundtrip_EventDurationAllDay(t *testing.T) {
	t.Parallel()
	original := event.Event{
		UID:           "duration-allday",
		Title:         "All-Day Duration",
		StartTime:     time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local),
		EndTime:       time.Date(2026, 4, 3, 0, 0, 0, 0, time.Local),
		AllDay:        true,
		DurationValue: "P2D",
		Status:        "CONFIRMED",
		Transp:        "OPAQUE",
		CreatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	exported := string(data)
	if !strings.Contains(exported, "DURATION:P2D") {
		t.Errorf("exported ICS missing DURATION:P2D\n%s", exported)
	}
	if strings.Contains(exported, "DTEND") {
		t.Errorf("exported ICS contains DTEND when DURATION is set for all-day event\n%s", exported)
	}
}
