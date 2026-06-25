package ical

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/recurrence"
)

const minimalEventICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:test-uid-1
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Minimal Event
END:VEVENT
END:VCALENDAR`

const fullEventICS = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:full-uid-1
DTSTAMP:20260401T100000Z
DTSTART;TZID=America/Sao_Paulo:20260401T140000
DTEND;TZID=America/Sao_Paulo:20260401T150000
SUMMARY:Full Event
DESCRIPTION:A detailed description
LOCATION:Room A
STATUS:TENTATIVE
TRANSP:TRANSPARENT
PRIORITY:3
CLASS:PRIVATE
URL:https://example.com/meeting
CATEGORIES:work,meeting
RRULE:FREQ=WEEKLY;COUNT=10
SEQUENCE:5
EXDATE:20260408T140000Z
RDATE:20260415T140000Z
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
DESCRIPTION:Reminder
END:VALARM
ATTENDEE;CN=Alice;PARTSTAT=ACCEPTED;ROLE=REQ-PARTICIPANT:mailto:alice@example.com
ORGANIZER;CN=Bob:mailto:bob@example.com
END:VEVENT
END:VCALENDAR`

const allDayEventICS = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:allday-uid
DTSTAMP:20260401T100000Z
DTSTART;VALUE=DATE:20260401
DTEND;VALUE=DATE:20260402
SUMMARY:All Day Event
END:VEVENT
END:VCALENDAR`

const minimalTodoICS = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTODO
UID:todo-uid-1
DTSTAMP:20260401T100000Z
SUMMARY:Test Todo
STATUS:NEEDS-ACTION
END:VTODO
END:VCALENDAR`

const fullTodoICS = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTODO
UID:todo-full-uid
DTSTAMP:20260401T100000Z
SUMMARY:Full Todo
DESCRIPTION:Todo description
LOCATION:Office
DUE:20260405T170000Z
DTSTART:20260401T090000Z
COMPLETED:20260403T120000Z
PERCENT-COMPLETE:100
STATUS:COMPLETED
PRIORITY:1
CLASS:CONFIDENTIAL
URL:https://example.com/task
CATEGORIES:dev,testing
SEQUENCE:3
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT30M
DESCRIPTION:Todo reminder
END:VALARM
END:VTODO
END:VCALENDAR`

const mixedICS = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:event-1
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Event One
END:VEVENT
BEGIN:VTODO
UID:todo-1
DTSTAMP:20260401T100000Z
SUMMARY:Todo One
STATUS:NEEDS-ACTION
END:VTODO
BEGIN:VEVENT
UID:event-2
DTSTAMP:20260401T100000Z
DTSTART:20260402T100000Z
DTEND:20260402T110000Z
SUMMARY:Event Two
END:VEVENT
END:VCALENDAR`

func TestImport_MinimalEvent(t *testing.T) {
	t.Parallel()
	result, err := ImportFile(strings.NewReader(minimalEventICS))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	if e.UID != "test-uid-1" {
		t.Errorf("UID = %q", e.UID)
	}
	if e.Title != "Minimal Event" {
		t.Errorf("Title = %q", e.Title)
	}
}

func TestImport_FullEvent(t *testing.T) {
	t.Parallel()
	result, err := ImportFile(strings.NewReader(fullEventICS))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	if e.Title != "Full Event" {
		t.Errorf("Title = %q", e.Title)
	}
	if e.Description != "A detailed description" {
		t.Errorf("Description = %q", e.Description)
	}
	if e.Location != "Room A" {
		t.Errorf("Location = %q", e.Location)
	}
	if e.Status != "TENTATIVE" {
		t.Errorf("Status = %q", e.Status)
	}
	if e.Transp != "TRANSPARENT" {
		t.Errorf("Transp = %q", e.Transp)
	}
	if e.Priority != 3 {
		t.Errorf("Priority = %d", e.Priority)
	}
	if e.Class != "PRIVATE" {
		t.Errorf("Class = %q", e.Class)
	}
	if e.URL != "https://example.com/meeting" {
		t.Errorf("URL = %q", e.URL)
	}
	if e.Timezone != "America/Sao_Paulo" {
		t.Errorf("Timezone = %q", e.Timezone)
	}
	if e.RecurrenceRule != "FREQ=WEEKLY;COUNT=10" {
		t.Errorf("RecurrenceRule = %q", e.RecurrenceRule)
	}
	if e.Sequence != 5 {
		t.Errorf("Sequence = %d", e.Sequence)
	}
	if e.Categories == "" {
		t.Error("Categories is empty")
	}
	if e.ExDates == "" {
		t.Error("ExDates is empty")
	}
	if e.RDates == "" {
		t.Error("RDates is empty")
	}
	if len(e.Alarms) != 1 {
		t.Errorf("Alarms = %d, want 1", len(e.Alarms))
	} else {
		if e.Alarms[0].Action != "DISPLAY" {
			t.Errorf("Alarm.Action = %q", e.Alarms[0].Action)
		}
		if e.Alarms[0].TriggerValue != "-PT15M" {
			t.Errorf("Alarm.Trigger = %q", e.Alarms[0].TriggerValue)
		}
	}
	if len(e.Attendees) < 2 {
		t.Errorf("Attendees = %d, want >= 2", len(e.Attendees))
	}
}

func TestImport_EventEXDATEAndRDATERespectTZID(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:tzid-exdate-rdate
DTSTAMP:20260401T100000Z
DTSTART;TZID=America/New_York:20260401T090000
DTEND;TZID=America/New_York:20260401T100000
RRULE:FREQ=WEEKLY;COUNT=3
EXDATE;TZID=America/New_York:20260408T090000
RDATE;TZID=America/New_York:20260422T090000
SUMMARY:TZID recurrence dates
END:VEVENT
END:VCALENDAR`

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	if e.ExDates != "2026-04-08T13:00:00Z" {
		t.Fatalf("ExDates = %q, want 2026-04-08T13:00:00Z", e.ExDates)
	}
	if e.RDates != "2026-04-22T13:00:00Z" {
		t.Fatalf("RDates = %q, want 2026-04-22T13:00:00Z", e.RDates)
	}
}

func TestImport_AllDayEvent(t *testing.T) {
	t.Parallel()
	result, _ := ImportFile(strings.NewReader(allDayEventICS))
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}
	if !result.Events[0].AllDay {
		t.Error("AllDay = false, want true")
	}
}

// Regression test for issue #64: all-day (VALUE=DATE) events must be stored
// at midnight UTC, independent of the importing host's timezone. Before the
// fix the importer built the date in time.Local and then called .UTC(), so the
// stored instant shifted by the host offset (e.g. under UTC+12 midnight local
// became 12:00Z the previous day), corrupting the calendar date and recurrence
// occurrences.
func TestImport_AllDayEvent_StoresMidnightUTC_RegardlessOfHostTZ(t *testing.T) {
	// Mutates time.Local, so this test cannot run in parallel.
	prevLocal := time.Local
	time.Local = time.FixedZone("UTC+12", 12*60*60)
	t.Cleanup(func() { time.Local = prevLocal })

	const recurringAllDayICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:allday-recur-1
DTSTAMP:20260401T100000Z
DTSTART;VALUE=DATE:20260401
DTEND;VALUE=DATE:20260402
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:All Day Recurring
END:VEVENT
END:VCALENDAR`

	result, err := ImportFile(strings.NewReader(recurringAllDayICS))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	evt := result.Events[0]

	if !evt.AllDay {
		t.Error("AllDay = false, want true")
	}

	wantStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	if !evt.StartTime.Equal(wantStart) {
		t.Errorf("StartTime = %s, want %s", evt.StartTime.Format(time.RFC3339), wantStart.Format(time.RFC3339))
	}
	if !evt.EndTime.Equal(wantEnd) {
		t.Errorf("EndTime = %s, want %s", evt.EndTime.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
	// Stored instant must be exactly midnight UTC, not a host-dependent offset.
	if h, m, s := evt.StartTime.UTC().Clock(); h != 0 || m != 0 || s != 0 {
		t.Errorf("StartTime UTC clock = %02d:%02d:%02d, want 00:00:00", h, m, s)
	}

	// Recurrence occurrences must stay on the correct UTC day.
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	expanded := recurrence.ExpandEvent(evt, from, to)
	if len(expanded) != 3 {
		t.Fatalf("expanded occurrences = %d, want 3", len(expanded))
	}
	for i, occ := range expanded {
		want := time.Date(2026, 4, 1+i, 0, 0, 0, 0, time.UTC)
		if !occ.InstanceTime.UTC().Equal(want) {
			t.Errorf("occurrence[%d] = %s, want %s", i,
				occ.InstanceTime.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}
}

// Regression test for issue #64 (Codex review follow-up): date-only
// EXDATE/RDATE values for all-day events must normalize to midnight UTC, the
// same as DTSTART. Otherwise, on a non-UTC host an EXDATE;VALUE=DATE would land
// on the wrong UTC day and fail to suppress the occurrence, and an
// RDATE;VALUE=DATE would be added at a host-shifted instant.
func TestImport_AllDayEXDATERDATE_NormalizeToUTC(t *testing.T) {
	// Mutates time.Local, so this test cannot run in parallel.
	prevLocal := time.Local
	time.Local = time.FixedZone("UTC+12", 12*60*60)
	t.Cleanup(func() { time.Local = prevLocal })

	const ics = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:allday-exdate-1
DTSTAMP:20260401T100000Z
DTSTART;VALUE=DATE:20260401
DTEND;VALUE=DATE:20260402
RRULE:FREQ=DAILY;COUNT=3
EXDATE;VALUE=DATE:20260402
RDATE;VALUE=DATE:20260410
END:VEVENT
END:VCALENDAR`

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	evt := result.Events[0]

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	expanded := recurrence.ExpandEvent(evt, from, to)

	got := make(map[string]bool, len(expanded))
	for _, occ := range expanded {
		got[occ.InstanceTime.UTC().Format(time.RFC3339)] = true
	}

	// EXDATE;VALUE=DATE:20260402 must suppress the 2026-04-02 occurrence.
	if got["2026-04-02T00:00:00Z"] {
		t.Error("2026-04-02T00:00:00Z occurrence was not suppressed by EXDATE;VALUE=DATE")
	}
	// The other RRULE occurrences must remain.
	for _, want := range []string{"2026-04-01T00:00:00Z", "2026-04-03T00:00:00Z"} {
		if !got[want] {
			t.Errorf("missing RRULE occurrence %s", want)
		}
	}
	// RDATE;VALUE=DATE:20260410 must add an occurrence at midnight UTC.
	if !got["2026-04-10T00:00:00Z"] {
		t.Errorf("RDATE;VALUE=DATE occurrence not added at 2026-04-10T00:00:00Z; got %v", got)
	}
}

func TestImport_MinimalTodo(t *testing.T) {
	t.Parallel()
	result, err := ImportFile(strings.NewReader(minimalTodoICS))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("todos = %d, want 1", len(result.Todos))
	}
	td := result.Todos[0]
	if td.UID != "todo-uid-1" {
		t.Errorf("UID = %q", td.UID)
	}
	if td.Summary != "Test Todo" {
		t.Errorf("Summary = %q", td.Summary)
	}
	if td.Status != "NEEDS-ACTION" {
		t.Errorf("Status = %q", td.Status)
	}
}

func TestImport_FullTodo(t *testing.T) {
	t.Parallel()
	result, _ := ImportFile(strings.NewReader(fullTodoICS))
	if len(result.Todos) != 1 {
		t.Fatalf("todos = %d", len(result.Todos))
	}
	td := result.Todos[0]
	if td.Summary != "Full Todo" {
		t.Errorf("Summary = %q", td.Summary)
	}
	if td.Description != "Todo description" {
		t.Errorf("Description = %q", td.Description)
	}
	if td.DueDate == "" {
		t.Error("DueDate is empty")
	}
	if td.CompletedAt == "" {
		t.Error("CompletedAt is empty")
	}
	if td.PercentComplete != 100 {
		t.Errorf("PercentComplete = %d", td.PercentComplete)
	}
	if td.Status != "COMPLETED" {
		t.Errorf("Status = %q", td.Status)
	}
	if td.Priority != 1 {
		t.Errorf("Priority = %d", td.Priority)
	}
	if td.Class != "CONFIDENTIAL" {
		t.Errorf("Class = %q", td.Class)
	}
	if len(td.Alarms) != 1 {
		t.Errorf("Alarms = %d, want 1", len(td.Alarms))
	}
}

func TestImport_PreservesEmailAlarmAttendees(t *testing.T) {
	t.Parallel()

	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:email-alarm-uid
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Imported EMAIL Alarm
BEGIN:VALARM
ACTION:EMAIL
TRIGGER:-PT1H
DESCRIPTION:Mail me
ATTENDEE;CN=Alice:mailto:alice@example.com
ATTENDEE:mailto:bob@example.com
END:VALARM
END:VEVENT
END:VCALENDAR`

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	if len(result.Events[0].Alarms) != 1 {
		t.Fatalf("alarms = %d, want 1", len(result.Events[0].Alarms))
	}
	if got := len(result.Events[0].Alarms[0].Attendees); got != 2 {
		t.Fatalf("EMAIL alarm attendees = %d, want 2", got)
	}
}

func TestImport_MixedEventsTodos(t *testing.T) {
	t.Parallel()
	result, err := ImportFile(strings.NewReader(mixedICS))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 2 {
		t.Errorf("events = %d, want 2", len(result.Events))
	}
	if len(result.Todos) != 1 {
		t.Errorf("todos = %d, want 1", len(result.Todos))
	}
}

func TestImport_MissingUID(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
SUMMARY:No UID
END:VEVENT
END:VCALENDAR`
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 0 {
		t.Errorf("events = %d, want 0 (missing UID should be skipped)", len(result.Events))
	}
}

func TestImport_RecurrenceID(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:recurring-1
DTSTAMP:20260401T100000Z
DTSTART:20260408T140000Z
DTEND:20260408T150000Z
SUMMARY:Override Instance
RECURRENCE-ID:20260408T140000Z
END:VEVENT
END:VCALENDAR`
	result, _ := ImportFile(strings.NewReader(ics))
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}
	if result.Events[0].RecurrenceID == "" {
		t.Error("RecurrenceID is empty")
	}
}

func TestImport_Timezone(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:tz-test
DTSTAMP:20260401T100000Z
DTSTART;TZID=Europe/London:20260401T140000
DTEND;TZID=Europe/London:20260401T150000
SUMMARY:London Event
END:VEVENT
END:VCALENDAR`
	result, _ := ImportFile(strings.NewReader(ics))
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}
	if result.Events[0].Timezone != "Europe/London" {
		t.Errorf("Timezone = %q, want Europe/London", result.Events[0].Timezone)
	}
}

func TestImport_Duration(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:dur-test
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DURATION:PT2H30M
SUMMARY:Duration Event
END:VEVENT
END:VCALENDAR`
	result, _ := ImportFile(strings.NewReader(ics))
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}
	e := result.Events[0]
	dur := e.EndTime.Sub(e.StartTime)
	want := 2*time.Hour + 30*time.Minute
	if dur != want {
		t.Errorf("Duration = %v, want %v", dur, want)
	}
}

func TestImport_Attach(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:attach-test
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Event With Attach
ATTACH;FMTTYPE=application/pdf:https://example.com/doc.pdf
ATTACH:https://example.com/notes.txt
END:VEVENT
END:VCALENDAR`
	result, _ := ImportFile(strings.NewReader(ics))
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}
	if len(result.Events[0].Attachments) != 2 {
		t.Fatalf("Attachments = %d, want 2", len(result.Events[0].Attachments))
	}
	if result.Events[0].Attachments[0].URI != "https://example.com/doc.pdf" {
		t.Errorf("Attach[0].URI = %q", result.Events[0].Attachments[0].URI)
	}
	if result.Events[0].Attachments[0].FmtType != "application/pdf" {
		t.Errorf("Attach[0].FmtType = %q", result.Events[0].Attachments[0].FmtType)
	}
}

func TestImport_RejectsOversizedInlineAttachment(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString(make([]byte, maxInlineAttachmentBytes+1))
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:oversized-attachment
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Oversized Attach
ATTACH;ENCODING=BASE64;FMTTYPE=application/octet-stream:` + encoded + `
END:VEVENT
END:VCALENDAR`

	_, err := ImportFile(strings.NewReader(ics))
	if err == nil {
		t.Fatal("ImportFile should reject oversized inline attachments")
	}
}

func TestImport_RejectsOversizedCalendarPayload(t *testing.T) {
	t.Parallel()

	oversizedDescription := strings.Repeat("A", maxImportBytes)
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:oversized-payload
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Oversized Payload
DESCRIPTION:` + oversizedDescription + `
END:VEVENT
END:VCALENDAR`

	_, err := ImportFile(strings.NewReader(ics))
	if err == nil {
		t.Fatal("ImportFile should reject oversized calendar payloads")
	}
}

func TestImport_Comment(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:comment-test
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Event With Comments
COMMENT:First comment
COMMENT:Second comment
END:VEVENT
END:VCALENDAR`
	result, _ := ImportFile(strings.NewReader(ics))
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}
	if len(result.Events[0].Comments) != 2 {
		t.Fatalf("Comments = %d, want 2", len(result.Events[0].Comments))
	}
	if result.Events[0].Comments[0] != "First comment" {
		t.Errorf("Comment[0] = %q", result.Events[0].Comments[0])
	}
}

func TestImport_SkippedComponentWarnings(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VTIMEZONE
TZID:America/New_York
BEGIN:STANDARD
DTSTART:19701101T020000
RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
TZNAME:EST
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:evt-1
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Kept Event
END:VEVENT
BEGIN:VJOURNAL
UID:journal-1
DTSTAMP:20260401T100000Z
SUMMARY:Journal Entry
END:VJOURNAL
BEGIN:VJOURNAL
UID:journal-2
DTSTAMP:20260401T100000Z
SUMMARY:Another Journal
END:VJOURNAL
BEGIN:VFREEBUSY
UID:fb-1
DTSTAMP:20260401T100000Z
DTSTART:20260401T000000Z
DTEND:20260402T000000Z
END:VFREEBUSY
END:VCALENDAR`
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Errorf("events = %d, want 1", len(result.Events))
	}
	// VTIMEZONE should NOT produce a warning.
	for _, w := range result.Warnings {
		if strings.Contains(w, "VTIMEZONE") {
			t.Errorf("unexpected VTIMEZONE warning: %q", w)
		}
	}
	// VJOURNAL is now parsed, so we expect 2 journals.
	if len(result.Journals) != 2 {
		t.Errorf("journals = %d, want 2", len(result.Journals))
	}
	// VJOURNAL should NOT produce a warning.
	for _, w := range result.Warnings {
		if strings.Contains(w, "VJOURNAL") {
			t.Errorf("unexpected VJOURNAL warning: %q", w)
		}
	}
	if len(result.FreeBusy) != 1 {
		t.Fatalf("freebusy = %d, want 1", len(result.FreeBusy))
	}
	foundFreebusy := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "VFREEBUSY") {
			foundFreebusy = true
		}
	}
	if foundFreebusy {
		t.Errorf("unexpected VFREEBUSY warning; warnings = %v", result.Warnings)
	}
}

func TestImport_TriggerTZID(t *testing.T) {
	t.Parallel()
	// TRIGGER;TZID=America/New_York:20260327T090000 should be resolved to UTC.
	// America/New_York in March is EDT (UTC-4), so 09:00 EDT = 13:00 UTC.
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:tzid-trigger-test
DTSTAMP:20260401T100000Z
DTSTART:20260327T140000Z
DTEND:20260327T150000Z
SUMMARY:Event with TZID trigger
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER;TZID=America/New_York:20260327T090000
DESCRIPTION:TZID alarm
END:VALARM
END:VEVENT
END:VCALENDAR`
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	if len(result.Events[0].Alarms) != 1 {
		t.Fatalf("alarms = %d, want 1", len(result.Events[0].Alarms))
	}
	trigger := result.Events[0].Alarms[0].TriggerValue
	want := "20260327T130000Z"
	if trigger != want {
		t.Errorf("TriggerValue = %q, want %q (TZID=America/New_York 09:00 EDT = 13:00 UTC)", trigger, want)
	}
}

func TestImport_TriggerTZID_UnknownTimezone(t *testing.T) {
	t.Parallel()
	// Unknown TZID should fall through to floating datetime parse.
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:tzid-unknown-test
DTSTAMP:20260401T100000Z
DTSTART:20260327T140000Z
DTEND:20260327T150000Z
SUMMARY:Event with unknown TZID trigger
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER;TZID=Fake/Zone:20260327T090000
DESCRIPTION:Unknown TZID alarm
END:VALARM
END:VEVENT
END:VCALENDAR`
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	if len(result.Events[0].Alarms) != 1 {
		t.Fatalf("alarms = %d, want 1", len(result.Events[0].Alarms))
	}
	// Falls through to floating parse: stored as-is (not resolved to UTC).
	trigger := result.Events[0].Alarms[0].TriggerValue
	want := "20260327T090000"
	if trigger != want {
		t.Errorf("TriggerValue = %q, want %q (unknown TZID should fall through to floating)", trigger, want)
	}
	// A warning should be emitted about the unknown TZID.
	foundWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "Fake/Zone") && strings.Contains(w, "unknown timezone") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("missing unknown TZID warning; warnings = %v", result.Warnings)
	}
}

func TestImport_RelatedTo(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:rel-test
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Child Event
RELATED-TO;RELTYPE=PARENT:parent-uid-123
RELATED-TO;RELTYPE=SIBLING:sibling-uid-456
END:VEVENT
END:VCALENDAR`
	result, _ := ImportFile(strings.NewReader(ics))
	if len(result.Events) != 1 {
		t.Fatalf("events = %d", len(result.Events))
	}
	if len(result.Events[0].Relations) != 2 {
		t.Fatalf("Relations = %d, want 2", len(result.Events[0].Relations))
	}
	if result.Events[0].Relations[0].RelType != "PARENT" {
		t.Errorf("Rel[0].RelType = %q", result.Events[0].Relations[0].RelType)
	}
	if result.Events[0].Relations[0].RelUID != "parent-uid-123" {
		t.Errorf("Rel[0].RelUID = %q", result.Events[0].Relations[0].RelUID)
	}
}

func TestImport_VJournal(t *testing.T) {
	t.Parallel()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		"BEGIN:VJOURNAL\r\n" +
		"UID:journal-basic\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T090000Z\r\n" +
		"SUMMARY:Daily Standup Notes\r\n" +
		"DESCRIPTION:Discussed sprint progress\r\n" +
		"STATUS:FINAL\r\n" +
		"END:VJOURNAL\r\n" +
		"END:VCALENDAR\r\n"
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Journals) != 1 {
		t.Fatalf("journals = %d, want 1", len(result.Journals))
	}
	j := result.Journals[0]
	if j.UID != "journal-basic" {
		t.Errorf("UID = %q", j.UID)
	}
	if j.Summary != "Daily Standup Notes" {
		t.Errorf("Summary = %q", j.Summary)
	}
	if j.Description != "Discussed sprint progress" {
		t.Errorf("Description = %q", j.Description)
	}
	if j.StartDate == "" {
		t.Error("StartDate is empty")
	}
	if j.Status != "FINAL" {
		t.Errorf("Status = %q, want FINAL", j.Status)
	}
}

func TestImport_VJournal_MissingUID(t *testing.T) {
	t.Parallel()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		"BEGIN:VJOURNAL\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:No UID Journal\r\n" +
		"END:VJOURNAL\r\n" +
		"END:VCALENDAR\r\n"
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Journals) != 0 {
		t.Errorf("journals = %d, want 0 (missing UID should be skipped)", len(result.Journals))
	}
	foundWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "VJOURNAL") && strings.Contains(w, "missing UID") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected warning about missing UID; warnings = %v", result.Warnings)
	}
}

func TestImport_VJournal_MultipleDescriptions(t *testing.T) {
	t.Parallel()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		"BEGIN:VJOURNAL\r\n" +
		"UID:journal-multi-desc\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:Multi Description\r\n" +
		"DESCRIPTION:First paragraph\r\n" +
		"DESCRIPTION:Second paragraph\r\n" +
		"END:VJOURNAL\r\n" +
		"END:VCALENDAR\r\n"
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Journals) != 1 {
		t.Fatalf("journals = %d, want 1", len(result.Journals))
	}
	desc := result.Journals[0].Description
	if desc != "First paragraph\n\nSecond paragraph" {
		t.Errorf("Description = %q, want joined with double newline", desc)
	}
}

func TestImport_VJournal_DateOnly(t *testing.T) {
	t.Parallel()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		"BEGIN:VJOURNAL\r\n" +
		"UID:journal-dateonly\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART;VALUE=DATE:20260401\r\n" +
		"SUMMARY:Date Only Journal\r\n" +
		"END:VJOURNAL\r\n" +
		"END:VCALENDAR\r\n"
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Journals) != 1 {
		t.Fatalf("journals = %d, want 1", len(result.Journals))
	}
	if result.Journals[0].StartDate != "2026-04-01" {
		t.Errorf("StartDate = %q, want 2026-04-01", result.Journals[0].StartDate)
	}
}

func TestImport_VJournal_OptionalDTSTART(t *testing.T) {
	t.Parallel()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		"BEGIN:VJOURNAL\r\n" +
		"UID:journal-no-dtstart\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:No Start Date\r\n" +
		"END:VJOURNAL\r\n" +
		"END:VCALENDAR\r\n"
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Journals) != 1 {
		t.Fatalf("journals = %d, want 1", len(result.Journals))
	}
	if result.Journals[0].StartDate != "" {
		t.Errorf("StartDate = %q, want empty", result.Journals[0].StartDate)
	}
}
