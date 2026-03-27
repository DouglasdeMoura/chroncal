package ical

import (
	"strings"
	"testing"
	"time"
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
