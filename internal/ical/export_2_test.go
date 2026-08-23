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

// TestExport_AlarmDurationWithoutRepeat guards RFC 5545 §3.8.6.3. DURATION
// MUST be paired with REPEAT. An alarm with Duration set but Repeat == 0 must
// not emit a bare DURATION. Strict CalDAV servers (e.g. Google) reject
// that with HTTP 400. That blocks the whole resource. This is the inverse of
// the bug fixed for bare REPEAT (issue #363).
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

// TestExport_AttendeeNameWithQuoteDoesNotFailExport guards the free-text
// parameter values (CN, FILENAME, FMTTYPE). The go-ical encoder rejects a
// parameter value that contains a double-quote. A stored attendee name or
// an attachment metadata field with a quote then failed the whole export
// batch. Export must strip the bytes the parameter grammar cannot carry.
func TestExport_AttendeeNameWithQuoteDoesNotFailExport(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "att-quote",
		Title:     "Meeting",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Attendees: []model.Attendee{
			{Name: `He said "ok"`, Email: "user@example.com", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
			{Name: "Broken\r\nName", Email: "other@example.com", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
		},
		Attachments: []model.Attachment{
			{Data: []byte("x"), Filename: `bad"name.txt`, FmtType: "text/plain"},
		},
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("ExportEvents error: %v", err)
	}
	ics := string(data)
	for _, want := range []string{"CN=He said ok", "CN=BrokenName", "FILENAME=badname.txt", "FMTTYPE=text/plain"} {
		if !strings.Contains(ics, want) {
			t.Errorf("output missing %q\noutput:\n%s", want, ics)
		}
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

// TestExport_TodoDurationWithoutStart guards issue #102. A stored VTODO with
// DURATION but no DTSTART (which go-ical's encoder rejects) must not abort the
// whole export batch. It must not drop every todo.
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
// search order (VTODO first) used to drop all content before the first VTODO
// in silence. The lead VEVENT was included. See issue #365.
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
// case but with a VJOURNAL at the start of the second stream before a VTODO.
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

// TestMergeCalendars_FreeBusyOnlySecondStream covers issue #422. When the
// second stream carries a VFREEBUSY component but no VEVENT/VTODO/VJOURNAL, the
// component-marker search must still find it. b's BEGIN:VCALENDAR header is
// then stripped. Otherwise b is appended verbatim. That nests a second
// VCALENDAR.
func TestMergeCalendars_FreeBusyOnlySecondStream(t *testing.T) {
	t.Parallel()
	a := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	b := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VFREEBUSY\r\nUID:fb1\r\nEND:VFREEBUSY\r\n" +
		"END:VCALENDAR\r\n")

	merged := string(MergeCalendars(a, b))
	if !strings.Contains(merged, "UID:e1") {
		t.Error("UID:e1 from first calendar is missing")
	}
	if !strings.Contains(merged, "BEGIN:VFREEBUSY") {
		t.Error("VFREEBUSY from second calendar was dropped")
	}
	if strings.Count(merged, "BEGIN:VCALENDAR") != 1 {
		t.Errorf("expected 1 VCALENDAR header, got %d", strings.Count(merged, "BEGIN:VCALENDAR"))
	}
	if strings.Count(merged, "END:VCALENDAR") != 1 {
		t.Errorf("expected 1 END:VCALENDAR, got %d", strings.Count(merged, "END:VCALENDAR"))
	}
}

// TestMergeCalendars_NoComponentsSecondStream covers issue #422: a second
// stream with no components at all must not contribute a nested VCALENDAR
// header to the merged output.
func TestMergeCalendars_NoComponentsSecondStream(t *testing.T) {
	t.Parallel()
	a := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	b := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n")

	merged := string(MergeCalendars(a, b))
	if !strings.Contains(merged, "UID:e1") {
		t.Error("UID:e1 from first calendar is missing")
	}
	if strings.Count(merged, "BEGIN:VCALENDAR") != 1 {
		t.Errorf("expected 1 VCALENDAR header, got %d", strings.Count(merged, "BEGIN:VCALENDAR"))
	}
	if strings.Count(merged, "END:VCALENDAR") != 1 {
		t.Errorf("expected 1 END:VCALENDAR, got %d", strings.Count(merged, "END:VCALENDAR"))
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

func TestExport_FloatingOverrideRecurrenceIDIsFloating(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		{
			UID:          "floating-recurring",
			Title:        "Workout (moved)",
			StartTime:    time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC),
			EndTime:      time.Date(2026, 4, 13, 15, 0, 0, 0, time.UTC),
			Timezone:     "FLOATING",
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
		t.Errorf("floating override RECURRENCE-ID must not carry VALUE=DATE, got %q", line)
	}
	if strings.Contains(line, "TZID") {
		t.Errorf("floating override RECURRENCE-ID must not carry TZID, got %q", line)
	}
	if v := recurrenceIDValue(line); strings.HasSuffix(v, "Z") {
		t.Errorf("floating override RECURRENCE-ID must not carry a Z suffix (must match floating DTSTART), got %q", line)
	}
	if !strings.Contains(recurrenceIDValue(line), "T") {
		t.Errorf("floating override RECURRENCE-ID must keep its time component, got %q", line)
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

// A stored row an older build wrote can hold a malformed action, because
// the constraint refuses an empty value alone. Export must not write it:
// a bare or malformed ACTION line is invalid iCal, and a strict server
// rejects the whole resource for it (issue #595).
func TestExport_MalformedStoredActionTakesTheReservedToken(t *testing.T) {
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	// An empty action is an unset value, so it takes the DISPLAY default.
	// A malformed non-empty action belongs to the VALARM of another
	// client, so it takes the reserved x-name. DISPLAY there would push
	// that alarm to the server as a firing reminder (issue #603).
	cases := map[string]string{
		"":      model.DefaultAlarmAction,
		" ":     model.UnsupportedAlarmAction,
		"\t":    model.UnsupportedAlarmAction,
		"NO NE": model.UnsupportedAlarmAction,
	}
	for action, want := range cases {
		evt := event.Event{
			UID: "malformed-action@example.com", Title: "Test",
			StartTime: start, EndTime: start.Add(time.Hour),
			Alarms: []model.Alarm{{Action: action, TriggerValue: "-PT15M", Related: "START"}},
		}
		raw, err := ExportEvents([]event.Event{evt}, "Work")
		if err != nil {
			t.Fatalf("ExportEvents(action %q): %v", action, err)
		}
		out := string(raw)
		if action != "" && strings.Contains(out, "ACTION:"+action) {
			t.Errorf("action %q: export emitted it verbatim:\n%s", action, out)
		}
		if !strings.Contains(out, "ACTION:"+want) {
			t.Errorf("action %q: export did not write %q:\n%s", action, want, out)
		}
	}
}
