package ical

import (
	"strings"
	"testing"
)

// An unparseable DUE or DTSTART on a VTODO is dropped. It cannot be stored.
// A drop of it in silence is data loss the user never sees. The todo leaves
// every due-date view. The next export re-emits the VTODO without it.
func TestImport_UnparseableTodoDates_Warn(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:bad-due@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DUE:2026-04-01\r\n" +
		"DTSTART:nonsense\r\n" +
		"SUMMARY:Todo with bad dates\r\n" +
		"END:VTODO\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("todos = %d, want 1", len(result.Todos))
	}
	var sawDue, sawStart bool
	for _, w := range result.Warnings {
		if strings.Contains(w, "DUE") {
			sawDue = true
		}
		if strings.Contains(w, "DTSTART") {
			sawStart = true
		}
	}
	if !sawDue {
		t.Errorf("no warning for the unparseable DUE; warnings = %v", result.Warnings)
	}
	if !sawStart {
		t.Errorf("no warning for the unparseable DTSTART; warnings = %v", result.Warnings)
	}
}

// A dropped-alarm warning must name the record that lost the alarm. The
// unparseable TRIGGER is dropped rather than stored. The warning is then the
// only trace. In a large import, "alarm dropped" with no owner is unactionable.
// Follows the parseDateProp precedent: `<kind> "<uid>": ...`.
func TestImport_DroppedAlarmWarning_NamesOwningRecord(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:alarm-owner-event@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with bad trigger\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:P\r\n" +
		"DESCRIPTION:Broken\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:alarm-owner-todo@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:Todo with bad trigger\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:soon\r\n" +
		"DESCRIPTION:Broken\r\n" +
		"END:VALARM\r\n" +
		"END:VTODO\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if !warningMentions(result.Warnings, `event "alarm-owner-event@example.com"`, "TRIGGER", `"P"`) {
		t.Errorf("event alarm warning does not name its owner; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, `todo "alarm-owner-todo@example.com"`, "TRIGGER", `"soon"`) {
		t.Errorf("todo alarm warning does not name its owner; warnings = %v", result.Warnings)
	}
}

// Regression test for issue #575. Google CalDAV emits VALARM ACTION:NONE as
// a "no reminder" sentinel. The alarm tables reject that action with a CHECK
// constraint. The failed insert rolled back the whole resource transaction,
// so the event never persisted and sync never converged. The parser must
// drop each unsupported alarm with a warning and keep the parent record and
// its supported alarms. (The todo caller shares the warning wrapper; see
// TestImport_DroppedAlarmWarning_NamesOwningRecord.)
func TestImport_UnsupportedAlarmAction_DropsAlarmKeepsRecord(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:action-none-event@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with a no-op alarm\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:NONE\r\n" +
		"TRIGGER:-PT15M\r\n" +
		"END:VALARM\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:PROCEDURE\r\n" +
		"TRIGGER:-PT5M\r\n" +
		"END:VALARM\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:-PT30M\r\n" +
		"DESCRIPTION:Real reminder\r\n" +
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
	if len(result.Events[0].Alarms) != 1 {
		t.Fatalf("event alarms = %d, want 1; alarms = %+v", len(result.Events[0].Alarms), result.Events[0].Alarms)
	}
	if got := result.Events[0].Alarms[0].Action; got != "DISPLAY" {
		t.Errorf("surviving alarm action = %q, want DISPLAY", got)
	}
	if !warningMentions(result.Warnings, `event "action-none-event@example.com"`, "ACTION", `"NONE"`) {
		t.Errorf("no ACTION NONE warning that names the event; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, `event "action-none-event@example.com"`, "ACTION", `"PROCEDURE"`) {
		t.Errorf("no ACTION PROCEDURE warning that names the event; warnings = %v", result.Warnings)
	}
}

// An unsupported RELATED value and a malformed DURATION share the failure
// class of issue #575. RELATED hits the CHECK constraint and rolls back the
// resource. DURATION round-trips into a push that a strict server rejects.
// The parser must degrade both with a warning and must not store them.
func TestImport_UnsupportedAlarmRelatedAndDuration_DegradeWithWarning(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:bad-related-event@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with a bad RELATED and DURATION\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER;RELATED=STARTS:-PT15M\r\n" +
		"REPEAT:2\r\n" +
		"DURATION:5 minutes\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 1 {
		t.Fatalf("events/alarms = %d/%v, want 1 event with 1 alarm", len(result.Events), result.Events)
	}
	alarm := result.Events[0].Alarms[0]
	if alarm.Related != "START" {
		t.Errorf("Related = %q, want the START default", alarm.Related)
	}
	if alarm.Duration != "" {
		t.Errorf("Duration = %q, want empty", alarm.Duration)
	}
	if alarm.Repeat != 0 {
		t.Errorf("Repeat = %d, want 0 (RFC 5545 pairs REPEAT with DURATION)", alarm.Repeat)
	}
	if !warningMentions(result.Warnings, `event "bad-related-event@example.com"`, "RELATED", "STARTS") {
		t.Errorf("no RELATED warning that names the event; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, `event "bad-related-event@example.com"`, "DURATION", `"5 minutes"`) {
		t.Errorf("no DURATION warning that names the event; warnings = %v", result.Warnings)
	}
}

// Three recoverable VALARM defects must degrade without a lost reminder:
// an empty ACTION keeps the DISPLAY default, a REPEAT with no DURATION is
// cleared (RFC 5545 pairs them), and a negative DURATION is dropped so the
// repeat triggers cannot walk backwards in time.
func TestImport_RecoverableAlarmDefects_KeepAlarm(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:recoverable-alarm-event@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with recoverable alarm defects\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:\r\n" +
		"TRIGGER:-PT15M\r\n" +
		"REPEAT:3\r\n" +
		"END:VALARM\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:-PT30M\r\n" +
		"REPEAT:2\r\n" +
		"DURATION:-PT5M\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 2 {
		t.Fatalf("events/alarms = %d/%+v, want 1 event with 2 alarms", len(result.Events), result.Events)
	}
	for i, alarm := range result.Events[0].Alarms {
		if alarm.Action != "DISPLAY" {
			t.Errorf("alarm %d: Action = %q, want DISPLAY", i, alarm.Action)
		}
		if alarm.Repeat != 0 || alarm.Duration != "" {
			t.Errorf("alarm %d: Repeat/Duration = %d/%q, want the cleared pair", i, alarm.Repeat, alarm.Duration)
		}
	}
	if !warningMentions(result.Warnings, "REPEAT and DURATION") {
		t.Errorf("no unpaired REPEAT warning; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, "DURATION", `"-PT5M"`) {
		t.Errorf("no negative DURATION warning; warnings = %v", result.Warnings)
	}
}

// parseAlarm reports every problem it finds. A single warning slot meant the
// ATTACH message overwrote the "will not fire" one. The user then saw a broken
// sound file and never learned the alarm was dead.
func TestImport_AlarmWithTwoProblems_ReportsBoth(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:two-alarm-problems@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T100000Z\r\n" +
		"DTEND:20260401T110000Z\r\n" +
		"SUMMARY:Doubly broken alarm\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:AUDIO\r\n" +
		"TRIGGER:not-a-time\r\n" +
		"ATTACH;ENCODING=BASE64;VALUE=BINARY:!!!not-base64!!!\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	joined := strings.Join(result.Warnings, " | ")
	if !strings.Contains(joined, "TRIGGER") {
		t.Errorf("TRIGGER warning lost; warnings = %v", result.Warnings)
	}
	if !strings.Contains(joined, "ATTACH") {
		t.Errorf("ATTACH warning lost; warnings = %v", result.Warnings)
	}
}
