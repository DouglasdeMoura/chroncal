package ical

import (
	"strings"
	"testing"
)

// The VTODO fix left VJOURNAL's DTSTART on the old silent-discard path. A
// journal with an unparseable DTSTART imports with no start date. It drops out
// of every date view. It re-exports without the property. Nothing in
// Warnings explains it.
func TestImport_UnparseableJournalDTSTART_Warns(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VJOURNAL\r\n" +
		"UID:bad-journal-dtstart@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:2026-04-01\r\n" +
		"SUMMARY:Journal with a bad DTSTART\r\n" +
		"END:VJOURNAL\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Journals) != 1 {
		t.Fatalf("journals = %d, want 1", len(result.Journals))
	}
	if !warningMentions(result.Warnings, "DTSTART") {
		t.Errorf("no warning for the unparseable DTSTART; warnings = %v", result.Warnings)
	}
}
