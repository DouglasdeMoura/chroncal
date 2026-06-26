package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
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

func TestExport_AllDayEvent_PositiveUTCOffset(t *testing.T) {
	t.Parallel()
	// Simulate a user in UTC+12 (e.g. Auckland) creating an all-day event
	// for April 15. Midnight local = April 14 12:00 UTC.
	// The exported date must be 20260415, not 20260414.
	loc := time.FixedZone("UTC+12", 12*60*60)
	events := []event.Event{{
		UID:       "allday-utcplus",
		Title:     "Auckland Day",
		StartTime: time.Date(2026, 4, 15, 0, 0, 0, 0, loc),
		EndTime:   time.Date(2026, 4, 16, 0, 0, 0, 0, loc),
		AllDay:    true,
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if !strings.Contains(ics, "DTSTART;VALUE=DATE:20260415") {
		t.Errorf("expected DTSTART date 20260415, got:\n%s", ics)
	}
	if !strings.Contains(ics, "DTEND;VALUE=DATE:20260416") {
		t.Errorf("expected DTEND date 20260416, got:\n%s", ics)
	}
}

func TestExport_AllDayEvent_StoredUTCInstantUsesLocalDate(t *testing.T) {
	prevLocal := time.Local
	time.Local = time.FixedZone("UTC+12", 12*60*60)
	t.Cleanup(func() { time.Local = prevLocal })

	// This is how a UTC-normalized all-day 2026-04-15 in UTC+12 is stored:
	// local midnight is the previous UTC date at 12:00. Export must preserve the
	// calendar date, not the UTC date of the stored instant.
	events := []event.Event{{
		UID:       "allday-stored-utc",
		Title:     "Stored Auckland Day",
		StartTime: time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		AllDay:    true,
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	ics := string(data)
	if !strings.Contains(ics, "DTSTART;VALUE=DATE:20260415") {
		t.Fatalf("expected DTSTART date 20260415, got:\n%s", ics)
	}
	if !strings.Contains(ics, "DTEND;VALUE=DATE:20260416") {
		t.Fatalf("expected DTEND date 20260416, got:\n%s", ics)
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

func TestExport_CategoriesNotEscaped(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:        "cat-export",
		Title:      "Category Event",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:     "CONFIRMED",
		Transp:     "OPAQUE",
		Categories: "meeting,work,urgent",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	// Commas must NOT be escaped — they are value separators in CATEGORIES
	if strings.Contains(ics, `meeting\,work`) {
		t.Errorf("CATEGORIES has escaped commas:\n%s", ics)
	}
	if !strings.Contains(ics, "CATEGORIES:meeting,work,urgent") {
		t.Errorf("expected unescaped CATEGORIES, got:\n%s", ics)
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

func TestExport_VTimezonePresent(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "vtz-export",
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

	if !strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Fatalf("missing VTIMEZONE block:\n%s", ics)
	}
	if !strings.Contains(ics, "TZID:America/New_York") {
		t.Error("missing TZID property in VTIMEZONE")
	}
	if !strings.Contains(ics, "TZOFFSETTO:") {
		t.Error("missing TZOFFSETTO in VTIMEZONE")
	}
	if !strings.Contains(ics, "TZOFFSETFROM:") {
		t.Error("missing TZOFFSETFROM in VTIMEZONE")
	}
	// America/New_York has DST, so both STANDARD and DAYLIGHT should be present
	if !strings.Contains(ics, "BEGIN:STANDARD") {
		t.Error("missing STANDARD sub-component")
	}
	if !strings.Contains(ics, "BEGIN:DAYLIGHT") {
		t.Error("missing DAYLIGHT sub-component")
	}
}

func TestExport_VTimezoneRecurringTransitions(t *testing.T) {
	t.Parallel()
	// An event years away from "now" must still get a VTIMEZONE whose
	// transitions cover its date. Emitting recurring RRULE transition rules
	// (rather than one-shot DTSTARTs in the current year) satisfies this.
	events := []event.Event{{
		UID:       "vtz-recurring",
		Title:     "Future TZ Event",
		StartTime: time.Date(2035, 7, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2035, 7, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if !strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Fatalf("missing VTIMEZONE block:\n%s", ics)
	}
	// DST transitions must be expressed as yearly recurrence rules so the
	// VTIMEZONE applies to every year, not just the export year.
	if !strings.Contains(ics, "RRULE:FREQ=YEARLY") {
		t.Errorf("VTIMEZONE missing recurring RRULE transition:\n%s", ics)
	}
	// America/New_York: DST begins the 2nd Sunday of March, ends the 1st
	// Sunday of November.
	if !strings.Contains(ics, "FREQ=YEARLY;BYMONTH=3;BYDAY=2SU") {
		t.Errorf("missing DAYLIGHT transition rule (2nd Sunday of March):\n%s", ics)
	}
	if !strings.Contains(ics, "FREQ=YEARLY;BYMONTH=11;BYDAY=1SU") {
		t.Errorf("missing STANDARD transition rule (1st Sunday of November):\n%s", ics)
	}
}

func TestExport_VTimezoneNoDST(t *testing.T) {
	t.Parallel()
	// Asia/Kolkata does not observe DST
	events := []event.Event{{
		UID:       "vtz-nodst",
		Title:     "No DST Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "Asia/Kolkata",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if !strings.Contains(ics, "TZID:Asia/Kolkata") {
		t.Error("missing TZID for Asia/Kolkata")
	}
	if !strings.Contains(ics, "BEGIN:STANDARD") {
		t.Error("missing STANDARD sub-component")
	}
	if strings.Contains(ics, "BEGIN:DAYLIGHT") {
		t.Error("Asia/Kolkata should not have DAYLIGHT sub-component")
	}
	if !strings.Contains(ics, "+0530") {
		t.Error("expected +0530 offset for Asia/Kolkata")
	}
}

func TestExport_NoVTimezoneWithoutTZID(t *testing.T) {
	t.Parallel()
	// Events without a timezone should not generate VTIMEZONE
	events := []event.Event{{
		UID:       "no-vtz",
		Title:     "UTC Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Error("UTC event should not generate VTIMEZONE")
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

// TestExport_AlarmRepeatWithoutDuration guards RFC 5545 §3.8.6.2: REPEAT
// MUST be paired with DURATION. A Repeat with no Duration must not emit a
// bare REPEAT, which strict CalDAV servers (e.g. Google) reject with 400.
func TestExport_AlarmRepeatWithoutDuration(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "alarm-repeat-no-duration",
		Title:     "Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "no interval", Repeat: 3},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if strings.Contains(ics, "REPEAT") {
		t.Errorf("emitted REPEAT without DURATION (non-conformant per RFC 5545 §3.8.6.2):\n%s", ics)
	}
}

// TestExport_AlarmRepeatWithDuration confirms the conformant pair still
// round-trips when both REPEAT and DURATION are present.
func TestExport_AlarmRepeatWithDuration(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "alarm-repeat-with-duration",
		Title:     "Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "repeat", Duration: "PT5M", Repeat: 3},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if !strings.Contains(ics, "REPEAT:3") {
		t.Errorf("missing REPEAT:3 when DURATION present:\n%s", ics)
	}
	if !strings.Contains(ics, "DURATION:PT5M") {
		t.Errorf("missing DURATION:PT5M:\n%s", ics)
	}
}

// TestExport_AlarmDurationWithoutRepeat guards RFC 5545 §3.8.6.3: DURATION
// MUST be paired with REPEAT. An alarm with Duration set but Repeat == 0 must
// not emit a bare DURATION, which strict CalDAV servers (e.g. Google) reject
// with HTTP 400, blocking the whole resource. This is the inverse of the bug
// fixed for bare REPEAT (issue #363).
func TestExport_AlarmDurationWithoutRepeat(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "alarm-duration-no-repeat",
		Title:     "Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "no repeat count", Duration: "PT5M"},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if strings.Contains(ics, "\nDURATION:") {
		t.Errorf("emitted DURATION without REPEAT (non-conformant per RFC 5545 §3.8.6.3):\n%s", ics)
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

// TestExport_TodoDurationWithoutStart guards issue #102: a stored VTODO with
// DURATION but no DTSTART (which go-ical's encoder rejects) must not abort the
// whole export batch and drop every todo.
func TestExport_TodoDurationWithoutStart(t *testing.T) {
	t.Parallel()
	todos := []todo.Todo{
		{
			UID:       "todo-bad-duration",
			Summary:   "Bad Duration",
			Status:    "NEEDS-ACTION",
			Duration:  "PT1H", // DURATION without DTSTART -> encoder rejects
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			UID:       "todo-good",
			Summary:   "Good Todo",
			Status:    "NEEDS-ACTION",
			DueDate:   "2026-04-05T17:00:00Z",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	data, err := ExportTodos(todos, "")
	if err != nil {
		t.Fatalf("ExportTodos error: %v", err)
	}
	ics := string(data)

	// The good todo must survive (no all-or-nothing failure).
	if !strings.Contains(ics, "UID:todo-good") {
		t.Errorf("good todo dropped; export aborted by malformed sibling")
	}
	// The malformed todo must still export, sanitized (DURATION dropped).
	if !strings.Contains(ics, "UID:todo-bad-duration") {
		t.Errorf("malformed todo missing from export")
	}
	if strings.Contains(ics, "DURATION:") {
		t.Errorf("DURATION without DTSTART should have been dropped, got:\n%s", ics)
	}
}

// TestExport_TodoDueAndDuration guards issue #102: DUE + DURATION together is
// rejected by the encoder; DURATION must be dropped so the batch still encodes.
func TestExport_TodoDueAndDuration(t *testing.T) {
	t.Parallel()
	todos := []todo.Todo{{
		UID:       "todo-due-and-duration",
		Summary:   "Due And Duration",
		Status:    "NEEDS-ACTION",
		DueDate:   "2026-04-05T17:00:00Z",
		Duration:  "PT1H",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, err := ExportTodos(todos, "")
	if err != nil {
		t.Fatalf("ExportTodos error: %v", err)
	}
	ics := string(data)
	if !strings.Contains(ics, "DUE:") {
		t.Errorf("DUE should be preserved")
	}
	if strings.Contains(ics, "DURATION:") {
		t.Errorf("DURATION should be dropped when DUE present, got:\n%s", ics)
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

func TestExport_MergeCalendars_PreservesNewVTIMEZONE(t *testing.T) {
	t.Parallel()
	a := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	b := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/London\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VTODO\r\nUID:t1\r\nEND:VTODO\r\n" +
		"END:VCALENDAR\r\n")

	merged := string(MergeCalendars(a, b))

	// The Europe/London timezone from b must be preserved.
	if !strings.Contains(merged, "TZID:Europe/London") {
		t.Error("missing VTIMEZONE Europe/London from second calendar")
	}
	// The duplicate America/New_York must NOT be added twice.
	if strings.Count(merged, "TZID:America/New_York") != 1 {
		t.Errorf("expected 1 America/New_York VTIMEZONE, got %d",
			strings.Count(merged, "TZID:America/New_York"))
	}
	if !strings.Contains(merged, "BEGIN:VEVENT") {
		t.Error("missing VEVENT from first calendar")
	}
	if !strings.Contains(merged, "BEGIN:VTODO") {
		t.Error("missing VTODO from second calendar")
	}
	if strings.Count(merged, "BEGIN:VCALENDAR") != 1 {
		t.Errorf("expected 1 VCALENDAR header, got %d", strings.Count(merged, "BEGIN:VCALENDAR"))
	}
}

// TestMergeCalendars_MixedSecondStream_EventBeforeTodo exercises the case where
// the second stream contains a VEVENT that precedes a VTODO. The if/else if
// search order (VTODO first) used to cause all content before the first VTODO
// to be silently dropped, including the leading VEVENT. See issue #365.
func TestMergeCalendars_MixedSecondStream_EventBeforeTodo(t *testing.T) {
	t.Parallel()
	a := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	// b: VEVENT (e2) appears before VTODO (t1) — e2 must survive the merge.
	b := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nUID:e2\r\nEND:VEVENT\r\n" +
		"BEGIN:VTODO\r\nUID:t1\r\nEND:VTODO\r\n" +
		"END:VCALENDAR\r\n")

	merged := string(MergeCalendars(a, b))
	if !strings.Contains(merged, "UID:e1") {
		t.Error("UID:e1 from first calendar is missing")
	}
	if !strings.Contains(merged, "UID:e2") {
		t.Error("UID:e2 (leading VEVENT of second calendar) was dropped")
	}
	if !strings.Contains(merged, "UID:t1") {
		t.Error("UID:t1 from second calendar is missing")
	}
	if strings.Count(merged, "BEGIN:VCALENDAR") != 1 {
		t.Errorf("expected 1 VCALENDAR header, got %d", strings.Count(merged, "BEGIN:VCALENDAR"))
	}
	if strings.Count(merged, "END:VCALENDAR") != 1 {
		t.Errorf("expected 1 END:VCALENDAR, got %d", strings.Count(merged, "END:VCALENDAR"))
	}
}

// TestMergeCalendars_MixedSecondStream_JournalBeforeTodo mirrors the event
// case but with a VJOURNAL leading the second stream before a VTODO.
func TestMergeCalendars_MixedSecondStream_JournalBeforeTodo(t *testing.T) {
	t.Parallel()
	a := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	b := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VJOURNAL\r\nUID:j1\r\nEND:VJOURNAL\r\n" +
		"BEGIN:VTODO\r\nUID:t1\r\nEND:VTODO\r\n" +
		"END:VCALENDAR\r\n")

	merged := string(MergeCalendars(a, b))
	if !strings.Contains(merged, "UID:j1") {
		t.Error("UID:j1 (leading VJOURNAL of second calendar) was dropped")
	}
	if !strings.Contains(merged, "UID:t1") {
		t.Error("UID:t1 from second calendar is missing")
	}
	if strings.Count(merged, "BEGIN:VCALENDAR") != 1 {
		t.Errorf("expected 1 VCALENDAR header, got %d", strings.Count(merged, "BEGIN:VCALENDAR"))
	}
}

func TestExport_MasterWithOverride(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		{
			UID:            "recurring-1",
			Title:          "Weekly Sync",
			StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC),
			EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
			Status:         "CONFIRMED",
			RecurrenceRule: "FREQ=WEEKLY;COUNT=4",
			ExDates:        "2026-04-20T09:00:00Z",
		},
		{
			UID:          "recurring-1",
			Title:        "Weekly Sync (moved)",
			StartTime:    time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC),
			EndTime:      time.Date(2026, 4, 13, 15, 0, 0, 0, time.UTC),
			Status:       "CONFIRMED",
			RecurrenceID: "2026-04-13T09:00:00Z",
		},
	}

	data, err := ExportEvents(events, "Test")
	if err != nil {
		t.Fatal(err)
	}
	ics := string(data)

	// Should have two VEVENT blocks with the same UID.
	if strings.Count(ics, "BEGIN:VEVENT") != 2 {
		t.Errorf("expected 2 VEVENTs, got %d", strings.Count(ics, "BEGIN:VEVENT"))
	}
	if !strings.Contains(ics, "UID:recurring-1") {
		t.Error("missing UID")
	}
	if !strings.Contains(ics, "RECURRENCE-ID") {
		t.Error("override should have RECURRENCE-ID")
	}
	if !strings.Contains(ics, "RRULE:FREQ=WEEKLY") {
		t.Error("master should have RRULE")
	}
	if !strings.Contains(ics, "EXDATE") {
		t.Error("master should have EXDATE")
	}
}

func TestExportJournals_Basic(t *testing.T) {
	t.Parallel()
	journals := []journal.Journal{{
		UID:       "journal-export-1",
		Summary:   "Test Journal",
		Status:    "FINAL",
		Class:     "PUBLIC",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}}

	data, err := ExportJournals(journals, "Test")
	if err != nil {
		t.Fatalf("ExportJournals error: %v", err)
	}
	ics := string(data)

	required := []string{
		"BEGIN:VCALENDAR", "END:VCALENDAR",
		"BEGIN:VJOURNAL", "END:VJOURNAL",
		"UID:journal-export-1", "SUMMARY:Test Journal",
		"STATUS:FINAL", "DTSTAMP:", "VERSION:2.0",
	}
	for _, s := range required {
		if !strings.Contains(ics, s) {
			t.Errorf("output missing %q", s)
		}
	}
}

func TestExportJournals_DateOnly(t *testing.T) {
	t.Parallel()
	journals := []journal.Journal{{
		UID:       "journal-dateonly-export",
		Summary:   "Date Only",
		StartDate: "2026-04-01",
		Status:    "FINAL",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}}

	data, err := ExportJournals(journals, "")
	if err != nil {
		t.Fatalf("ExportJournals error: %v", err)
	}
	ics := string(data)

	if !strings.Contains(ics, "VALUE=DATE") {
		t.Error("date-only journal missing VALUE=DATE")
	}
	// Verify the date value is YYYYMMDD format, not containing "T"
	for _, line := range strings.Split(ics, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.Contains(line, "VALUE=DATE") && strings.Contains(line, "DTSTART") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && strings.Contains(parts[1], "T") {
				t.Errorf("VALUE=DATE line contains time component: %s", line)
			}
		}
	}
}

// recurrenceIDLine returns the unfolded RECURRENCE-ID property line from an
// exported iCalendar payload, or "" if none is present.
func recurrenceIDLine(ics string) string {
	for _, line := range strings.Split(ics, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "RECURRENCE-ID") {
			return line
		}
	}
	return ""
}

// recurrenceIDValue returns the value portion (after the colon) of a
// RECURRENCE-ID property line.
func recurrenceIDValue(line string) string {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func TestExport_AllDayOverrideRecurrenceIDIsDate(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		{
			UID:            "allday-recurring",
			Title:          "Daily Standup",
			StartTime:      time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
			EndTime:        time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC),
			AllDay:         true,
			Status:         "CONFIRMED",
			RecurrenceRule: "FREQ=DAILY;COUNT=4",
		},
		{
			UID:          "allday-recurring",
			Title:        "Daily Standup (moved)",
			StartTime:    time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
			EndTime:      time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC),
			AllDay:       true,
			Status:       "CONFIRMED",
			RecurrenceID: "2026-04-15T00:00:00Z",
		},
	}

	data, err := ExportEvents(events, "Test")
	if err != nil {
		t.Fatal(err)
	}
	line := recurrenceIDLine(string(data))
	if line == "" {
		t.Fatal("missing RECURRENCE-ID")
	}
	if !strings.Contains(line, "VALUE=DATE") {
		t.Errorf("all-day override RECURRENCE-ID must carry VALUE=DATE, got %q", line)
	}
	if strings.Contains(recurrenceIDValue(line), "T") {
		t.Errorf("all-day override RECURRENCE-ID must be date-only (no time component), got %q", line)
	}
}

func TestExport_TimedOverrideRecurrenceIDIsDateTime(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		{
			UID:          "timed-recurring",
			Title:        "Weekly Sync (moved)",
			StartTime:    time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC),
			EndTime:      time.Date(2026, 4, 13, 15, 0, 0, 0, time.UTC),
			Status:       "CONFIRMED",
			RecurrenceID: "2026-04-13T09:00:00Z",
		},
	}
	data, err := ExportEvents(events, "Test")
	if err != nil {
		t.Fatal(err)
	}
	line := recurrenceIDLine(string(data))
	if line == "" {
		t.Fatal("missing RECURRENCE-ID")
	}
	if strings.Contains(line, "VALUE=DATE") {
		t.Errorf("timed override RECURRENCE-ID must not carry VALUE=DATE, got %q", line)
	}
	if !strings.Contains(recurrenceIDValue(line), "T") {
		t.Errorf("timed override RECURRENCE-ID must keep its time component, got %q", line)
	}
}

func TestExport_AllDayTodoOverrideRecurrenceIDIsDate(t *testing.T) {
	t.Parallel()
	todos := []todo.Todo{
		{
			UID:          "allday-todo-recurring",
			Summary:      "Daily Task (moved)",
			DueDate:      "2026-04-15",
			RecurrenceID: "2026-04-15T00:00:00Z",
		},
	}
	data, err := ExportTodos(todos, "Test")
	if err != nil {
		t.Fatal(err)
	}
	line := recurrenceIDLine(string(data))
	if line == "" {
		t.Fatal("missing RECURRENCE-ID")
	}
	if !strings.Contains(line, "VALUE=DATE") {
		t.Errorf("all-day todo override RECURRENCE-ID must carry VALUE=DATE, got %q", line)
	}
	if strings.Contains(recurrenceIDValue(line), "T") {
		t.Errorf("all-day todo override RECURRENCE-ID must be date-only, got %q", line)
	}
}

func TestExport_AllDayJournalOverrideRecurrenceIDIsDate(t *testing.T) {
	t.Parallel()
	journals := []journal.Journal{
		{
			UID:          "allday-journal-recurring",
			Summary:      "Daily Note (moved)",
			StartDate:    "2026-04-15",
			RecurrenceID: "2026-04-15T00:00:00Z",
		},
	}
	data, err := ExportJournals(journals, "Test")
	if err != nil {
		t.Fatal(err)
	}
	line := recurrenceIDLine(string(data))
	if line == "" {
		t.Fatal("missing RECURRENCE-ID")
	}
	if !strings.Contains(line, "VALUE=DATE") {
		t.Errorf("all-day journal override RECURRENCE-ID must carry VALUE=DATE, got %q", line)
	}
	if strings.Contains(recurrenceIDValue(line), "T") {
		t.Errorf("all-day journal override RECURRENCE-ID must be date-only, got %q", line)
	}
}
