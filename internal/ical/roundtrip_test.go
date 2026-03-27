package ical

import (
	"bytes"
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
