package ical

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

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

// TestRoundtrip_FloatingClockHostIndependent verifies that floating-time
// values export the stored wall clock unchanged, regardless of the host's
// local timezone. Import interprets a floating DTSTART as UTC. That stores the
// wall clock as e.g. 09:00:00Z. Export must then format the UTC wall clock,
// not e.Local(). Otherwise the displayed clock shifts on non-UTC hosts.
func TestRoundtrip_FloatingClockHostIndependent(t *testing.T) {
	// Force a non-UTC host timezone so .Local() would visibly shift the clock.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	orig := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = orig })

	t.Run("event", func(t *testing.T) {
		data, err := ExportEvents([]event.Event{{
			UID:       "floating-event",
			Title:     "Floating",
			StartTime: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC),
			Timezone:  "FLOATING",
			Status:    "CONFIRMED",
			Transp:    "OPAQUE",
		}}, "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		exported := string(data)
		if !strings.Contains(exported, "DTSTART:20260401T090000") {
			t.Errorf("floating DTSTART clock shifted by host TZ; want DTSTART:20260401T090000\n%s", exported)
		}
		if !strings.Contains(exported, "DTEND:20260401T093000") {
			t.Errorf("floating DTEND clock shifted by host TZ; want DTEND:20260401T093000\n%s", exported)
		}
	})

	t.Run("todo", func(t *testing.T) {
		data, err := ExportTodos([]todo.Todo{{
			UID:       "floating-todo",
			Summary:   "Floating",
			StartDate: "2026-04-01T09:00:00Z",
			DueDate:   "2026-04-01T17:00:00Z",
			Timezone:  "FLOATING",
			Status:    "NEEDS-ACTION",
		}}, "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		exported := string(data)
		if !strings.Contains(exported, "DTSTART:20260401T090000") {
			t.Errorf("floating todo DTSTART clock shifted by host TZ; want DTSTART:20260401T090000\n%s", exported)
		}
		if !strings.Contains(exported, "DUE:20260401T170000") {
			t.Errorf("floating todo DUE clock shifted by host TZ; want DUE:20260401T170000\n%s", exported)
		}
	})

	t.Run("journal", func(t *testing.T) {
		data, err := ExportJournals([]journal.Journal{{
			UID:       "floating-journal",
			Summary:   "Floating",
			StartDate: "2026-04-01T09:00:00Z",
			Timezone:  "FLOATING",
			Status:    "FINAL",
		}}, "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		exported := string(data)
		if !strings.Contains(exported, "DTSTART:20260401T090000") {
			t.Errorf("floating journal DTSTART clock shifted by host TZ; want DTSTART:20260401T090000\n%s", exported)
		}
	})
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

func TestRoundtrip_Journal(t *testing.T) {
	t.Parallel()
	original := journal.Journal{
		UID:            "roundtrip-journal",
		Summary:        "Roundtrip Journal",
		Description:    "Testing journal round-trip",
		StartDate:      "2026-04-01T09:00:00Z",
		Status:         "FINAL",
		Class:          "PRIVATE",
		URL:            "https://example.com/journal",
		Categories:     "notes",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
		Sequence:       2,
		ExDates:        "2026-04-03T09:00:00Z",
		RDates:         "2026-04-10T09:00:00Z",
		Comments:       []string{"First comment", "Second comment"},
		Attachments: []model.Attachment{
			{URI: "https://example.com/doc.pdf", FmtType: "application/pdf"},
		},
		Relations: []model.Relation{
			{RelType: "PARENT", RelUID: "parent-uid-123"},
		},
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportJournals([]journal.Journal{original}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Journals) != 1 {
		t.Fatalf("reimported %d journals", len(result.Journals))
	}

	got := result.Journals[0]

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
	if got.Status != original.Status {
		t.Errorf("Status: %q != %q", got.Status, original.Status)
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

	// StartDate
	if got.StartDate == "" {
		t.Error("StartDate lost on round-trip")
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

// TestRoundtrip_JournalMultipleDescriptions guards against a collapse of a
// VJOURNAL's multiple DESCRIPTION properties (permitted by RFC 5545) into a
// single concatenated value (issue #493). N descriptions must survive import ->
// export as N distinct DESCRIPTION properties. That matches COMMENT/CONTACT.
func TestRoundtrip_JournalMultipleDescriptions(t *testing.T) {
	t.Parallel()
	const ics = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VJOURNAL
UID:multi-desc-journal
DTSTAMP:20260401T100000Z
DTSTART:20260401T090000Z
SUMMARY:Multiple Descriptions
DESCRIPTION:First description block
DESCRIPTION:Second description block
END:VJOURNAL
END:VCALENDAR`

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Journals) != 1 {
		t.Fatalf("imported %d journals, want 1", len(result.Journals))
	}
	if got := len(result.Journals[0].Descriptions); got != 2 {
		t.Errorf("Descriptions: got %d, want 2 (%q)", got, result.Journals[0].Descriptions)
	}

	data, err := ExportJournals(result.Journals, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	count := strings.Count(string(data), "DESCRIPTION:")
	if count != 2 {
		t.Errorf("exported DESCRIPTION properties: got %d, want 2\n%s", count, data)
	}
}

// TestRoundtrip_CategoryWithEmbeddedComma guards against a split of a single
// CATEGORIES value that legitimately contains a comma (issue #101). A category
// like "Foo, Bar" must survive import -> store -> export -> import as one value.
// It must not split into two categories on every round-trip.
func TestRoundtrip_CategoryWithEmbeddedComma(t *testing.T) {
	t.Parallel()
	const ics = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:cat-comma
DTSTAMP:20260401T100000Z
DTSTART:20260401T140000Z
DTEND:20260401T150000Z
SUMMARY:Comma Category
CATEGORIES:Foo\, Bar,Baz
END:VEVENT
END:VCALENDAR`

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("imported %d events, want 1", len(result.Events))
	}
	if got := result.Events[0].ParseCategories(); len(got) != 2 ||
		got[0] != "Foo, Bar" || got[1] != "Baz" {
		t.Fatalf("imported categories = %v, want [\"Foo, Bar\" \"Baz\"]", got)
	}

	// Round-trip through export and re-import: the comma-bearing category must
	// still be a single value.
	data, err := ExportEvents(result.Events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	reimported, err := ImportFile(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if len(reimported.Events) != 1 {
		t.Fatalf("reimported %d events, want 1", len(reimported.Events))
	}
	got := reimported.Events[0].ParseCategories()
	if len(got) != 2 || got[0] != "Foo, Bar" || got[1] != "Baz" {
		t.Fatalf("round-tripped categories = %v, want [\"Foo, Bar\" \"Baz\"]", got)
	}
}

// Regression test for issue #579. A VALARM with an action outside the
// fireable set must survive an import → export round trip verbatim. That
// covers the Google ACTION:NONE sentinel and an RFC 5545 x-name action
// from another client. A drop would make the next push delete the alarm
// server-side. The ATTACH of an x-name action must survive too.
func TestRoundtrip_UnsupportedAlarmActionsSurvive(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:sync-only-alarms@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with foreign alarms\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:NONE\r\n" +
		"TRIGGER;VALUE=DATE-TIME:19760401T005545Z\r\n" +
		"END:VALARM\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:X-Apple-Sound\r\n" +
		"TRIGGER:-PT9M\r\n" +
		"ATTACH:Chord\r\n" +
		"REPEAT:5000\r\n" +
		"DURATION:PT5M\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 2 {
		t.Fatalf("events/alarms = %d/%+v, want 1 event with 2 alarms", len(result.Events), result.Events)
	}

	data, err := ExportEvents(result.Events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	out := string(data)
	for _, want := range []string{
		"ACTION:NONE",
		"TRIGGER;VALUE=DATE-TIME:19760401T005545Z",
		"ACTION:X-Apple-Sound",
		"TRIGGER;VALUE=DURATION:-PT9M",
		"ATTACH:Chord",
		// The clamp guards the check loop, which never expands a
		// sync-only alarm. A clamp here would rewrite the count of
		// another client on the next push.
		"REPEAT:5000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export lacks %q; output:\n%s", want, out)
		}
	}

	// The second pass must parse the same two alarms again: the loop must
	// be stable, not merely one-shot.
	reimported, err := ImportFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if len(reimported.Events) != 1 || len(reimported.Events[0].Alarms) != 2 {
		t.Fatalf("reimported events/alarms = %d/%+v, want 1 event with 2 alarms",
			len(reimported.Events), reimported.Events)
	}
	// The mixed-case x-name action must keep its original case: an
	// uppercased copy would rewrite the VALARM of the other client on
	// the next push.
	gotActions := []string{reimported.Events[0].Alarms[0].Action, reimported.Events[0].Alarms[1].Action}
	if gotActions[0] != "NONE" || gotActions[1] != "X-Apple-Sound" {
		t.Errorf("reimported actions = %v, want [NONE X-Apple-Sound]", gotActions)
	}
	if got := reimported.Events[0].Alarms[1].Repeat; got != 5000 {
		t.Errorf("reimported repeat = %d, want 5000 (a preserved alarm keeps its count)", got)
	}
}

// The REPEAT clamp still applies to a fireable alarm. That alarm expands
// into per-trigger state in the check loop, so an absurd count must not
// reach the engine. This is the counterpart of the exemption that
// TestRoundtrip_UnsupportedAlarmActionsSurvive pins.
func TestRoundtrip_FireableAlarmRepeatStillClamps(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:clamped-alarm@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with an absurd repeat\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:-PT9M\r\n" +
		"REPEAT:5000\r\n" +
		"DURATION:PT5M\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 1 {
		t.Fatalf("events/alarms = %d/%+v, want 1 event with 1 alarm", len(result.Events), result.Events)
	}
	if got := result.Events[0].Alarms[0].Repeat; got != model.MaxAlarmRepeat {
		t.Errorf("imported repeat = %d, want the clamp %d", got, model.MaxAlarmRepeat)
	}

	data, err := ExportEvents(result.Events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if want := "REPEAT:" + strconv.Itoa(model.MaxAlarmRepeat); !strings.Contains(string(data), want) {
		t.Errorf("export lacks %q; output:\n%s", want, string(data))
	}
}

// A VALARM property the parser does not read must survive the round trip.
// The parser used to keep only an X- property, so an IANA property such as
// the RFC 9074 PROXIMITY was lost, and the next push rewrote the VALARM of
// another client without it (issue #586, item f).
func TestRoundtrip_UnhandledAlarmPropertiesSurvive(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:iana-alarm-props@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with an IANA alarm property\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:-PT15M\r\n" +
		"DESCRIPTION:Reminder\r\n" +
		"PROXIMITY:DEPART\r\n" +
		"X-VENDOR-FLAG:keep-me\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 1 {
		t.Fatalf("events/alarms = %d/%+v, want 1 event with 1 alarm", len(result.Events), result.Events)
	}

	data, err := ExportEvents(result.Events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	out := string(data)
	for _, want := range []string{"PROXIMITY:DEPART", "X-VENDOR-FLAG:keep-me"} {
		if !strings.Contains(out, want) {
			t.Errorf("export lacks %q; output:\n%s", want, out)
		}
	}
	// The parser still reads the properties it owns, so they must not
	// also appear a second time as preserved properties.
	if n := strings.Count(out, "DESCRIPTION:Reminder"); n != 1 {
		t.Errorf("DESCRIPTION appears %d times, want 1", n)
	}
	if n := strings.Count(out, "TRIGGER"); n != 1 {
		t.Errorf("TRIGGER appears %d times, want 1", n)
	}
}
