package ical

import (
	"strings"
	"testing"
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
