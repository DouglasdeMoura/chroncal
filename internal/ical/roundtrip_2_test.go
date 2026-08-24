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

func TestRoundtrip_AlarmAttachBinary(t *testing.T) {
	t.Parallel()
	// AUDIO alarm with an inline BASE64 ATTACH (embedded sound) should
	// roundtrip without losing the binary payload (issue #298).
	sound := []byte("fake-wav-bytes\x00\x01\x02\xff")
	original := event.Event{
		UID:       "roundtrip-alarm-attach-binary",
		Title:     "Event with Embedded Audio Alarm",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Alarms: []model.Alarm{
			{
				Action:        "AUDIO",
				TriggerValue:  "-PT15M",
				Description:   "Alarm",
				AttachBinary:  sound,
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

	ics := string(data)
	if !strings.Contains(ics, "ENCODING=BASE64") {
		t.Errorf("exported ICS missing ENCODING=BASE64:\n%s", ics)
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
	if !bytes.Equal(alarm.AttachBinary, sound) {
		t.Errorf("AttachBinary: got %v, want %v", alarm.AttachBinary, sound)
	}
	if alarm.AttachFmtType != "audio/basic" {
		t.Errorf("AttachFmtType: got %q, want %q", alarm.AttachFmtType, "audio/basic")
	}
	if alarm.AttachURI != "" {
		t.Errorf("AttachURI: got %q, want empty", alarm.AttachURI)
	}
}

func TestRoundtrip_AlarmAttachEMAIL(t *testing.T) {
	t.Parallel()
	// EMAIL alarms may carry one or more ATTACH attachments (RFC 5545 §3.6.6).
	// An imported EMAIL attachment must not be silently dropped on export
	// (issue #468).
	original := event.Event{
		UID:       "roundtrip-alarm-attach-email",
		Title:     "Event with Email Alarm Attachment",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Alarms: []model.Alarm{
			{
				Action:        "EMAIL",
				TriggerValue:  "-PT1H",
				Summary:       "Heads up",
				Description:   "Meeting soon",
				AttachURI:     "http://example.com/files/agenda.pdf",
				AttachFmtType: "application/pdf",
				Attendees: []model.AlarmAttendee{
					{Email: "user@example.com"},
				},
			},
		},
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	ics := string(data)
	if !strings.Contains(ics, "ATTACH") {
		t.Errorf("exported ICS missing ATTACH for EMAIL alarm:\n%s", ics)
	}
	if !strings.Contains(ics, "FMTTYPE=application/pdf") {
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
	if alarm.AttachURI != "http://example.com/files/agenda.pdf" {
		t.Errorf("AttachURI: got %q, want %q", alarm.AttachURI, "http://example.com/files/agenda.pdf")
	}
	if alarm.AttachFmtType != "application/pdf" {
		t.Errorf("AttachFmtType: got %q, want %q", alarm.AttachFmtType, "application/pdf")
	}
}
