package ical

import (
	"strings"
	"testing"
	"time"
)

// A malformed DTEND must not cost an all-day event its RFC 5545 implicit
// one-day span. Treat the bad value as an explicit end and EndTime collapsed
// back to StartTime. Every range query is half-open
// (start_time < ? AND end_time > ?). A zero-length event then matches no window.
// The import vanishes from every day/week/month view in silence.
func TestImport_MalformedDTEND_AllDayKeepsOneDaySpan(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:bad-dtend-allday@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART;VALUE=DATE:20260401\r\n" +
		"DTEND;VALUE=DATE:2026-04-05\r\n" +
		"SUMMARY:All-day with bad DTEND\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	if !e.AllDay {
		t.Fatalf("AllDay = false, want true")
	}
	want := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	if !e.EndTime.Equal(want) {
		t.Errorf("EndTime = %v, want %v (implicit one-day span)", e.EndTime, want)
	}
	if !e.EndTime.After(e.StartTime) {
		t.Errorf("EndTime %v is not after StartTime %v; a zero-length event matches no range query",
			e.EndTime, e.StartTime)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected a warning about the unparseable DTEND")
	}
}

// A malformed DTEND must not shadow a perfectly good DURATION on the same
// component. Force the 1h fallback before the DURATION branch ran. That
// shortened the event in silence. It dropped the value from the round-trip.
func TestImport_MalformedDTEND_FallsBackToDuration(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:bad-dtend-duration@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T100000Z\r\n" +
		"DTEND:garbage\r\n" +
		"DURATION:PT3H\r\n" +
		"SUMMARY:Bad DTEND, good DURATION\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	want := time.Date(2026, 4, 1, 13, 0, 0, 0, time.UTC)
	if !e.EndTime.Equal(want) {
		t.Errorf("EndTime = %v, want %v (DURATION:PT3H)", e.EndTime, want)
	}
	if e.DurationValue != "PT3H" {
		t.Errorf("DurationValue = %q, want %q", e.DurationValue, "PT3H")
	}
}

// With neither a usable DTEND nor a DURATION, a timed event still gets the 1h
// default. It does not collapse to zero duration.
func TestImport_MalformedDTEND_TimedFallsBackToOneHour(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:bad-dtend-timed@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T100000Z\r\n" +
		"DTEND:garbage\r\n" +
		"SUMMARY:Bad DTEND only\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	want := time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC)
	if !e.EndTime.Equal(want) {
		t.Errorf("EndTime = %v, want %v (1h default)", e.EndTime, want)
	}
}
