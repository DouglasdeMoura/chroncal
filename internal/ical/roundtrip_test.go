package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/todo"
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
}

func TestRoundtrip_Todo(t *testing.T) {
	t.Parallel()
	original := todo.Todo{
		UID:             "roundtrip-todo",
		Summary:         "Roundtrip Todo",
		Description:     "Test todo roundtrip",
		Location:        "Office",
		DueDate:         "2026-04-05T17:00:00Z",
		Status:          "IN-PROCESS",
		Priority:        3,
		PercentComplete: 50,
		URL:             "https://example.com/task",
		Categories:      "dev",
		Sequence:        1,
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
	if got.UID != original.UID {
		t.Errorf("UID: %q != %q", got.UID, original.UID)
	}
	if got.Summary != original.Summary {
		t.Errorf("Summary: %q != %q", got.Summary, original.Summary)
	}
	if got.Status != original.Status {
		t.Errorf("Status: %q != %q", got.Status, original.Status)
	}
	if got.Priority != original.Priority {
		t.Errorf("Priority: %d != %d", got.Priority, original.Priority)
	}
	if got.PercentComplete != original.PercentComplete {
		t.Errorf("PercentComplete: %d != %d", got.PercentComplete, original.PercentComplete)
	}
	if got.DueDate == "" {
		t.Error("DueDate lost on round-trip")
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

	if len(got.Attendees) < 2 {
		t.Errorf("Attendees: %d, want >= 2", len(got.Attendees))
	}
}
