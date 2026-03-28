package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/todo"
)

func TestExport_SingleEvent(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "export-1",
		Title:     "Test Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Class:     "PUBLIC",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}}

	data, err := ExportEvents(events, "Test")
	if err != nil {
		t.Fatalf("ExportEvents error: %v", err)
	}
	ics := string(data)

	required := []string{"BEGIN:VCALENDAR", "END:VCALENDAR", "BEGIN:VEVENT", "END:VEVENT",
		"UID:export-1", "SUMMARY:Test Event", "DTSTAMP:", "DTSTART:", "DTEND:", "VERSION:2.0"}
	for _, s := range required {
		if !strings.Contains(ics, s) {
			t.Errorf("output missing %q", s)
		}
	}
}

func TestExport_EventAllFields(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:            "full-export-1",
		Title:          "Full Event",
		Description:    "A description",
		Location:       "Room B",
		StartTime:      time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:         "TENTATIVE",
		Transp:         "TRANSPARENT",
		Sequence:       3,
		Priority:       5,
		Class:          "PRIVATE",
		URL:            "https://example.com",
		Categories:     "work,meeting",
		RecurrenceRule: "FREQ=DAILY",
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	ics := string(data)

	checks := []string{
		"STATUS:TENTATIVE", "TRANSP:TRANSPARENT", "SEQUENCE:3", "PRIORITY:5",
		"CLASS:PRIVATE", "URL:https://example.com", "CATEGORIES:work",
		"DESCRIPTION:A description", "LOCATION:Room B", "RRULE:FREQ=DAILY",
	}
	for _, s := range checks {
		if !strings.Contains(ics, s) {
			t.Errorf("missing %q", s)
		}
	}
}

func TestExport_AllDayEvent(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "allday-export",
		Title:     "All Day",
		StartTime: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		AllDay:    true,
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if !strings.Contains(ics, "VALUE=DATE") {
		t.Error("all-day event missing VALUE=DATE")
	}
	// Bug 3: VALUE=DATE must use YYYYMMDD format, no time component.
	for _, line := range strings.Split(ics, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.Contains(line, "VALUE=DATE") {
			// The value after the colon must be exactly 8 digits (YYYYMMDD).
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && strings.Contains(parts[1], "T") {
				t.Errorf("VALUE=DATE line contains time component: %s", line)
			}
		}
	}
}

func TestExport_MultipleExdatesRdates(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "multi-exdate",
		Title:     "Recurring",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		ExDates:   "2026-04-08T14:00:00Z,2026-04-15T14:00:00Z",
		RDates:    "2026-05-01T14:00:00Z,2026-05-08T14:00:00Z",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	ics := string(data)

	if strings.Count(ics, "EXDATE") != 2 {
		t.Errorf("expected 2 EXDATE properties, got %d\n%s", strings.Count(ics, "EXDATE"), ics)
	}
	if strings.Count(ics, "RDATE") != 2 {
		t.Errorf("expected 2 RDATE properties, got %d\n%s", strings.Count(ics, "RDATE"), ics)
	}
	if !strings.Contains(ics, "20260408") {
		t.Error("missing first EXDATE (2026-04-08)")
	}
	if !strings.Contains(ics, "20260415") {
		t.Error("missing second EXDATE (2026-04-15)")
	}
}

func TestExport_AttendeePartstatNotEmpty(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "partstat-test",
		Title:     "Meeting",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Attendees: []model.Attendee{
			{Email: "user@example.com", Name: "User", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if strings.Contains(ics, "PARTSTAT=;") || strings.Contains(ics, "PARTSTAT=\r") {
		t.Errorf("PARTSTAT is empty in output:\n%s", ics)
	}
	if !strings.Contains(ics, "PARTSTAT=NEEDS-ACTION") {
		t.Errorf("expected PARTSTAT=NEEDS-ACTION in output:\n%s", ics)
	}
}

func TestExport_EventWithTimezone(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "tz-export",
		Title:     "TZ Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if !strings.Contains(ics, "TZID=America/New_York") {
		t.Error("missing TZID parameter")
	}
}

func TestExport_EventWithAlarms(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "alarm-export",
		Title:     "Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 min"},
			{Action: "EMAIL", TriggerValue: "-PT1H", Description: "1 hour"},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if strings.Count(ics, "BEGIN:VALARM") != 2 {
		t.Errorf("expected 2 VALARMs, got %d", strings.Count(ics, "BEGIN:VALARM"))
	}
	if !strings.Contains(ics, "ACTION:DISPLAY") {
		t.Error("missing ACTION:DISPLAY")
	}
}

func TestExport_EventWithAttendees(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "att-export",
		Title:     "Meeting",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Attendees: []model.Attendee{
			{Email: "org@example.com", Name: "Org", RSVPStatus: "ACCEPTED", Role: "CHAIR", Organizer: true},
			{Email: "user@example.com", Name: "User", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if !strings.Contains(ics, "ORGANIZER") {
		t.Error("missing ORGANIZER")
	}
	if !strings.Contains(ics, "ATTENDEE") {
		t.Error("missing ATTENDEE")
	}
	if !strings.Contains(ics, "mailto:org@example.com") {
		t.Error("missing organizer email")
	}
}

func TestExport_SingleTodo(t *testing.T) {
	t.Parallel()
	todos := []todo.Todo{{
		UID:       "todo-export-1",
		Summary:   "Test Todo",
		Status:    "NEEDS-ACTION",
		DueDate:   "2026-04-05T17:00:00Z",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, err := ExportTodos(todos, "")
	if err != nil {
		t.Fatalf("ExportTodos error: %v", err)
	}
	ics := string(data)

	required := []string{"BEGIN:VTODO", "END:VTODO", "UID:todo-export-1", "SUMMARY:Test Todo",
		"STATUS:NEEDS-ACTION", "DUE:"}
	for _, s := range required {
		if !strings.Contains(ics, s) {
			t.Errorf("missing %q", s)
		}
	}
}

func TestExport_TodoAllFields(t *testing.T) {
	t.Parallel()
	todos := []todo.Todo{{
		UID:             "todo-full-export",
		Summary:         "Full Todo",
		Description:     "Notes",
		Location:        "Office",
		DueDate:         "2026-04-05T17:00:00Z",
		CompletedAt:     "2026-04-03T12:00:00Z",
		PercentComplete: 100,
		Status:          "COMPLETED",
		Priority:        1,
		Class:           "CONFIDENTIAL",
		URL:             "https://example.com/task",
		Categories:      "dev",
		Sequence:        2,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}}

	data, _ := ExportTodos(todos, "")
	ics := string(data)

	checks := []string{"COMPLETED:", "PERCENT-COMPLETE:100", "PRIORITY:1", "CLASS:CONFIDENTIAL",
		"URL:https://example.com/task", "CATEGORIES:dev", "SEQUENCE:2"}
	for _, s := range checks {
		if !strings.Contains(ics, s) {
			t.Errorf("missing %q", s)
		}
	}
}

func TestExport_MergeCalendars(t *testing.T) {
	t.Parallel()
	a := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	b := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:t1\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")

	merged := string(MergeCalendars(a, b))
	if !strings.Contains(merged, "BEGIN:VEVENT") {
		t.Error("missing VEVENT")
	}
	if !strings.Contains(merged, "BEGIN:VTODO") {
		t.Error("missing VTODO")
	}
	if strings.Count(merged, "BEGIN:VCALENDAR") != 1 {
		t.Errorf("expected 1 VCALENDAR header, got %d", strings.Count(merged, "BEGIN:VCALENDAR"))
	}
	if strings.Count(merged, "END:VCALENDAR") != 1 {
		t.Errorf("expected 1 VCALENDAR footer, got %d", strings.Count(merged, "END:VCALENDAR"))
	}
}

func TestExport_MergeCalendars_EmptySecond(t *testing.T) {
	t.Parallel()
	a := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	b := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n")

	merged := string(MergeCalendars(a, b))
	if !strings.Contains(merged, "BEGIN:VEVENT") {
		t.Error("missing VEVENT from first calendar")
	}
}
