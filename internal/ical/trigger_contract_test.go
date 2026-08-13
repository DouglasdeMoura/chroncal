package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// An unparseable TRIGGER is dropped at import, with a warning.
//
// Preserving the raw value looked like it protected round-trip fidelity, but it
// never did: the value cannot be expressed as valid iCal, so the next push
// either wedges the resource (if emitted) or drops the VALARM server-side (if
// skipped). Preserving only moved that loss from "announced at import" to
// "silent at the next push", and left an alarm the UI presents as armed while
// every trigger-time helper refuses it. Losing it loudly is the honest trade.
func TestImport_UnparseableTrigger_DroppedWithWarning(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:bad-trigger-test\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with bad trigger\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:not-a-time\r\n" +
		"DESCRIPTION:Broken trigger alarm\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	if n := len(result.Events[0].Alarms); n != 0 {
		t.Errorf("alarms = %d, want 0: an alarm that can never fire must not be "+
			"stored, or the UI shows it as an armed reminder", n)
	}
	if !warningMentions(result.Warnings, "TRIGGER", "not-a-time") {
		t.Errorf("no warning naming the dropped trigger; warnings = %v", result.Warnings)
	}
}

func TestImport_UnparseableTodoTrigger_DroppedWithWarning(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:todo-bad-trigger-test\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:Todo with bad trigger\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:soon\r\n" +
		"DESCRIPTION:Broken trigger alarm\r\n" +
		"END:VALARM\r\n" +
		"END:VTODO\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("todos = %d, want 1", len(result.Todos))
	}
	if n := len(result.Todos[0].Alarms); n != 0 {
		t.Errorf("alarms = %d, want 0", n)
	}
	if !warningMentions(result.Warnings, "TRIGGER", "soon") {
		t.Errorf("no warning naming the dropped trigger; warnings = %v", result.Warnings)
	}
}

// A TRIGGER whose value is RFC 3339 (non-conforming exporters emit these) is
// accepted by parseAlarm's RFC 3339 fallback when it has no TZID param. Adding
// a TZID param must not turn the same value into a dropped alarm: the TZID
// branch of the validation ladder only tried the compact iCal layout, and a
// parse failure there declared the trigger invalid without ever reaching the
// RFC 3339 fallback. Under the drop policy (issue #570) that destroys the
// alarm on both sides of the next sync. An RFC 3339 value carries its own
// offset, so the TZID is redundant for interpretation; it is normalized to
// UTC compact form the way the compact+TZID path is.
func TestImport_RFC3339TriggerWithTZID_Kept(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:rfc3339-tzid-trigger@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with RFC 3339 trigger under TZID\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER;TZID=Europe/Berlin:2026-04-01T10:00:00+02:00\r\n" +
		"DESCRIPTION:Reminder\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	alarms := result.Events[0].Alarms
	if len(alarms) != 1 {
		t.Fatalf("alarms = %d, want 1 (RFC 3339 trigger under TZID must be kept); warnings = %v",
			len(alarms), result.Warnings)
	}
	// The stored form must resolve to the instant the original denoted:
	// 2026-04-01T10:00:00+02:00 == 2026-04-01T08:00:00Z. ParseAbsoluteTime is
	// the single source of truth for what a stored trigger string means.
	got, err := model.ParseAbsoluteTime(alarms[0].TriggerValue, "")
	if err != nil {
		t.Fatalf("stored trigger %q does not resolve: %v", alarms[0].TriggerValue, err)
	}
	want := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("stored trigger %q resolves to %v, want %v", alarms[0].TriggerValue, got, want)
	}
}

// RFC 5545 §3.8.6.3: an absolute (date-time) TRIGGER has no relation to the
// event's start or end — the trigabs production forbids the RELATED parameter
// entirely. A RELATED=END smuggled in alongside an absolute trigger is
// therefore unexpressible junk: export can never emit it (strict servers 400
// the PUT), so a push+pull cycle silently resets it to START, and if the user
// later switches the trigger to a duration the stale END resurfaces and moves
// when the alarm fires. Import must not store it: the parsed alarm keeps the
// default Related "START".
func TestImport_AbsoluteTriggerWithRelatedEnd_RelatedNotStored(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:abs-trigger-related-end@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with absolute trigger carrying RELATED=END\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER;VALUE=DATE-TIME;RELATED=END:20260401T130000Z\r\n" +
		"DESCRIPTION:Reminder\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	alarms := result.Events[0].Alarms
	if len(alarms) != 1 {
		t.Fatalf("alarms = %d, want 1; warnings = %v", len(alarms), result.Warnings)
	}
	if got := alarms[0].Related; got != "START" {
		t.Errorf("Related = %q, want %q: an absolute trigger has no relation, "+
			"and the wire cannot express one — storing it makes local state "+
			"depend on sync history", got, "START")
	}
	if got := alarms[0].TriggerValue; got != "20260401T130000Z" {
		t.Errorf("TriggerValue = %q, want %q", got, "20260401T130000Z")
	}
}

// Pin the existing behavior the previous test must not regress: RELATED=END on
// a duration trigger is meaningful (it anchors the offset to the event's end)
// and must survive import.
func TestImport_DurationTriggerWithRelatedEnd_RelatedPreserved(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:dur-trigger-related-end@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with duration trigger anchored to END\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER;RELATED=END:-PT5M\r\n" +
		"DESCRIPTION:Wrap-up reminder\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	alarms := result.Events[0].Alarms
	if len(alarms) != 1 {
		t.Fatalf("alarms = %d, want 1; warnings = %v", len(alarms), result.Warnings)
	}
	if got := alarms[0].Related; got != "END" {
		t.Errorf("Related = %q, want %q (duration triggers legitimately anchor to END)", got, "END")
	}
	if got := alarms[0].TriggerValue; got != "-PT5M" {
		t.Errorf("TriggerValue = %q, want %q", got, "-PT5M")
	}
}

func warningMentions(warnings []string, parts ...string) bool {
	for _, w := range warnings {
		all := true
		for _, p := range parts {
			if !strings.Contains(w, p) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
