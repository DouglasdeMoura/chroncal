package freebusy

import (
	"strings"
	"testing"
	"time"
)

func TestParseCalendar_ParsesAbsoluteAndDurationPeriods(t *testing.T) {
	t.Parallel()

	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VFREEBUSY
UID:fb-1
DTSTAMP:20260403T120000Z
DTSTART:20260410T000000Z
DTEND:20260411T000000Z
ORGANIZER:mailto:owner@example.com
FREEBUSY:20260410T090000Z/20260410T100000Z
FREEBUSY;FBTYPE=BUSY-TENTATIVE:20260410T110000Z/PT30M
END:VFREEBUSY
END:VCALENDAR`

	results, err := ParseCalendar(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ParseCalendar: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	result := results[0]
	if result.UID != "fb-1" {
		t.Fatalf("UID = %q, want fb-1", result.UID)
	}
	if result.Organizer != "mailto:owner@example.com" {
		t.Fatalf("Organizer = %q", result.Organizer)
	}
	if len(result.Periods) != 2 {
		t.Fatalf("periods = %d, want 2", len(result.Periods))
	}
	if result.Periods[0].Type != Busy {
		t.Fatalf("period[0].type = %q, want %q", result.Periods[0].Type, Busy)
	}
	if !result.Periods[1].End.Equal(time.Date(2026, 4, 10, 11, 30, 0, 0, time.UTC)) {
		t.Fatalf("period[1].end = %s, want 11:30 UTC", result.Periods[1].End)
	}
	if result.Periods[1].Type != BusyTentative {
		t.Fatalf("period[1].type = %q, want %q", result.Periods[1].Type, BusyTentative)
	}
}
