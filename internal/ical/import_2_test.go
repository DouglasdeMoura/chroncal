package ical

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"
)

// A malformed DTEND (go-ical stores the raw value with no validate) must not
// collapse the event to zero duration in silence. The old code did that
// (endTime, _ = Props.DateTime). Mirror the malformed-DURATION treatment
// (TestImport_MalformedDuration). Fall back to a 1h span and record a warning.
// The user then learns the end was dropped. They do not read back a bogus
// instantaneous event on re-export.
func TestImport_MalformedDTEND_WarnsAndFallsBack(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:bad-dtend-test
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:not-a-time
SUMMARY:Bad DTEND Event
END:VEVENT
END:VCALENDAR`
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	if !e.EndTime.After(e.StartTime) {
		t.Errorf("EndTime = %s is not after StartTime = %s; want sane fallback",
			e.EndTime.Format(time.RFC3339), e.StartTime.Format(time.RFC3339))
	}
	if want := e.StartTime.Add(time.Hour); !e.EndTime.Equal(want) {
		t.Errorf("EndTime = %s, want fallback %s", e.EndTime.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	hasWarning := slices.ContainsFunc(result.Warnings, func(w string) bool {
		return strings.Contains(strings.ToLower(w), "dtend")
	})
	if !hasWarning {
		t.Errorf("no DTEND warning recorded; warnings = %v", result.Warnings)
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

// TestImport_CustomVTimezone_PreservesZoneLabel covers issue #131. A TZID that
// is neither IANA nor a Windows alias (a private VTIMEZONE) must keep its zone
// identity on import. Previously resolveComponentTZIDs converted the value to
// UTC and dropped the TZID param. The event was then stored with an empty
// Timezone (a plain UTC event, in silence). The instant must stay
// correct AND the original TZID label must be preserved.
func TestImport_CustomVTimezone_PreservesZoneLabel(t *testing.T) {
	t.Parallel()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		"BEGIN:VTIMEZONE\r\n" +
		"TZID:Custom/Office\r\n" +
		"BEGIN:STANDARD\r\n" +
		"DTSTART:19700101T000000\r\n" +
		"TZOFFSETFROM:+0100\r\n" +
		"TZOFFSETTO:+0100\r\n" +
		"TZNAME:OFC\r\n" +
		"END:STANDARD\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:custom-tz-1\r\n" +
		"DTSTAMP:20240115T100000Z\r\n" +
		"DTSTART;TZID=Custom/Office:20240115T120000\r\n" +
		"DTEND;TZID=Custom/Office:20240115T130000\r\n" +
		"SUMMARY:Custom Zone Event\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	if e.Timezone != "Custom/Office" {
		t.Errorf("Timezone = %q, want %q", e.Timezone, "Custom/Office")
	}
	// +0100 local 12:00 == 11:00 UTC; the instant must be preserved.
	wantStart := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)
	if !e.StartTime.UTC().Equal(wantStart) {
		t.Errorf("StartTime = %v, want %v", e.StartTime.UTC(), wantStart)
	}
	wantEnd := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	if !e.EndTime.UTC().Equal(wantEnd) {
		t.Errorf("EndTime = %v, want %v", e.EndTime.UTC(), wantEnd)
	}
	// The VTIMEZONE block must be retained for storage/export.
	var found bool
	for _, tz := range result.Timezones {
		if tz.TZID == "Custom/Office" {
			found = true
		}
	}
	if !found {
		t.Errorf("VTIMEZONE Custom/Office not captured in result.Timezones")
	}
}

// A large day span is valid when the end still lands in the storable
// range. The day cap in internal/duration exists for overflow safety
// alone, so it must not replace such a span with the 1h fallback and
// push that value back over the server copy (issue #582 round 5).
func TestImport_LargeDaySpan_KeepsTheDuration(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:large-day-span
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DURATION:P400000D
SUMMARY:Long Span Event
END:VEVENT
END:VCALENDAR`
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	if e.DurationValue != "P400000D" {
		t.Errorf("DurationValue = %q, want P400000D kept", e.DurationValue)
	}
	if want := e.StartTime.AddDate(0, 0, 400000); !e.EndTime.Equal(want) {
		t.Errorf("EndTime = %s, want %s", e.EndTime, want)
	}
}

// RFC 5545 §3.6.2 makes DUE and DURATION mutually exclusive and needs a
// DTSTART beside a DURATION. The todo service rejects both shapes, so
// import must drop the DURATION and warn. A stored shape would fail the
// whole calendar pull on every run (issue #582 round 5).
func TestImport_TodoStructuralDuration_DroppedWithWarning(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTODO
UID:due-plus-duration
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DUE:20260402T140000Z
DURATION:PT1H
SUMMARY:Due And Duration
END:VTODO
BEGIN:VTODO
UID:duration-without-start
DTSTAMP:20260401T100000Z
DURATION:PT1H
SUMMARY:Duration Without Start
END:VTODO
END:VCALENDAR`
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Todos) != 2 {
		t.Fatalf("todos = %d, want 2", len(result.Todos))
	}
	for _, td := range result.Todos {
		if td.Duration != "" {
			t.Errorf("todo %q: Duration = %q, want it dropped", td.UID, td.Duration)
		}
	}
	joined := strings.Join(result.Warnings, " | ")
	if !strings.Contains(joined, "mutually exclusive") {
		t.Errorf("no DUE+DURATION warning; warnings = %v", result.Warnings)
	}
	if !strings.Contains(joined, "needs a DTSTART") {
		t.Errorf("no DURATION-without-DTSTART warning; warnings = %v", result.Warnings)
	}
}

// A DURATION whose end passes the storable year is dropped at import. The
// todo service rejects such a span in validateTiming, and the sync engine
// stops at the first resource error, so a stored value would fail the
// whole calendar pull on every run (issue #585).
func TestImport_TodoUnstorableDuration_DroppedWithWarning(t *testing.T) {
	t.Parallel()
	ics := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTODO
UID:unstorable-duration
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DURATION:P3000000D
SUMMARY:Unstorable Span
END:VTODO
END:VCALENDAR`
	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile error: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("todos = %d, want 1", len(result.Todos))
	}
	if result.Todos[0].Duration != "" {
		t.Errorf("Duration = %q, want it dropped", result.Todos[0].Duration)
	}
	joined := strings.Join(result.Warnings, " | ")
	if !strings.Contains(joined, "ends past year") {
		t.Errorf("no unstorable-DURATION warning; warnings = %v", result.Warnings)
	}
}
