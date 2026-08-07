package ical

import (
	"strings"
	"testing"
)

// An unparseable DUE or DTSTART on a VTODO is dropped — it cannot be stored —
// but dropping it silently is data loss the user never sees: the todo leaves
// every due-date view and the next export re-emits the VTODO without it.
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

// parseAlarm reports every problem it finds. A single warning slot meant the
// ATTACH message overwrote the "will not fire" one, so the user saw a broken
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
