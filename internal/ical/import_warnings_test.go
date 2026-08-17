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
// drop the unsupported alarm with a warning and keep the parent record and
// its supported alarms.
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
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:-PT30M\r\n" +
		"DESCRIPTION:Real reminder\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:action-proc-todo@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:Todo with a legacy alarm\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:PROCEDURE\r\n" +
		"TRIGGER:-PT5M\r\n" +
		"END:VALARM\r\n" +
		"END:VTODO\r\n" +
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
	if len(result.Todos) != 1 {
		t.Fatalf("todos = %d, want 1", len(result.Todos))
	}
	if len(result.Todos[0].Alarms) != 0 {
		t.Errorf("todo alarms = %d, want 0", len(result.Todos[0].Alarms))
	}
	if !warningMentions(result.Warnings, `event "action-none-event@example.com"`, "ACTION", `"NONE"`) {
		t.Errorf("no ACTION NONE warning that names the event; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, `todo "action-proc-todo@example.com"`, "ACTION", `"PROCEDURE"`) {
		t.Errorf("no ACTION PROCEDURE warning that names the todo; warnings = %v", result.Warnings)
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
