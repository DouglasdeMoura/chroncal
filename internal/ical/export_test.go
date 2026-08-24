package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// recurrenceIDLine returns the unfolded RECURRENCE-ID property line from an
// exported iCalendar payload, or "" if none is present.
func recurrenceIDLine(ics string) string {
	for _, line := range strings.Split(ics, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "RECURRENCE-ID") {
			return line
		}
	}
	return ""
}

// recurrenceIDValue returns the value portion (after the colon) of a
// RECURRENCE-ID property line.
func recurrenceIDValue(line string) string {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func TestExport_SingleEvent(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "export-1",
		Title:     "Test Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Class:     "PUBLIC",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}}

	data, err := ExportEvents(events, "Test")
	if err != nil {
		t.Fatalf("ExportEvents error: %v", err)
	}
	ics := string(data)

	required := []string{"BEGIN:VCALENDAR", "END:VCALENDAR", "BEGIN:VEVENT", "END:VEVENT",
		"UID:export-1", "SUMMARY:Test Event", "DTSTAMP:", "DTSTART:", "DTEND:", "VERSION:2.0"}
	for _, s := range required {
		if !strings.Contains(ics, s) {
			t.Errorf("output missing %q", s)
		}
	}
}

// A legacy stored negative DURATION must not reach the server. Export
// falls back to the stored end time as DTEND (issue #582 round 4). The
// startup heal clears such values, so this guard covers only a row the
// heal has not seen yet.
// A legacy row holds the end time that its bad span produced, so the
// end can precede the start. Export must emit neither the invalid
// DURATION nor an inverted interval (issue #582 round 4).
func TestExport_NegativeStoredDurationFallsBackToPositiveSpan(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC)
	events := []event.Event{{
		UID:   "export-neg-duration",
		Title: "Legacy negative span",
		// The real legacy shape: end = start + (-PT1H).
		StartTime:     start,
		EndTime:       start.Add(-time.Hour),
		DurationValue: "-PT1H",
		Status:        "CONFIRMED",
		Transp:        "OPAQUE",
		Class:         "PUBLIC",
	}}

	data, err := ExportEvents(events, "Test")
	if err != nil {
		t.Fatalf("ExportEvents error: %v", err)
	}
	ics := string(data)

	if strings.Contains(ics, "DURATION:") {
		t.Errorf("output carries the invalid DURATION; want the DTEND fallback:\n%s", ics)
	}
	if !strings.Contains(ics, "DTEND:20260401T150000Z") {
		t.Errorf("output missing the 1h fallback DTEND; an inverted interval is invalid iCal:\n%s", ics)
	}
	if strings.Contains(ics, "DTEND:20260401T130000Z") {
		t.Errorf("output carries an inverted DTEND before the DTSTART:\n%s", ics)
	}
}

func TestExport_EventAllFields(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:            "full-export-1",
		Title:          "Full Event",
		Description:    "A description",
		Location:       "Room B",
		StartTime:      time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:         "TENTATIVE",
		Transp:         "TRANSPARENT",
		Sequence:       3,
		Priority:       5,
		Class:          "PRIVATE",
		URL:            "https://example.com",
		Categories:     "work,meeting",
		RecurrenceRule: "FREQ=DAILY",
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	ics := string(data)

	checks := []string{
		"STATUS:TENTATIVE", "TRANSP:TRANSPARENT", "SEQUENCE:3", "PRIORITY:5",
		"CLASS:PRIVATE", "URL:https://example.com", "CATEGORIES:work",
		"DESCRIPTION:A description", "LOCATION:Room B", "RRULE:FREQ=DAILY",
	}
	for _, s := range checks {
		if !strings.Contains(ics, s) {
			t.Errorf("missing %q", s)
		}
	}
}

func TestExport_AllDayEvent(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "allday-export",
		Title:     "All Day",
		StartTime: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		AllDay:    true,
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if !strings.Contains(ics, "VALUE=DATE") {
		t.Error("all-day event missing VALUE=DATE")
	}
	// Bug 3: VALUE=DATE must use YYYYMMDD format, no time component.
	for _, line := range strings.Split(ics, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.Contains(line, "VALUE=DATE") {
			// The value after the colon must be exactly 8 digits (YYYYMMDD).
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && strings.Contains(parts[1], "T") {
				t.Errorf("VALUE=DATE line contains time component: %s", line)
			}
		}
	}
}

func TestExport_AllDayEvent_PositiveUTCOffset(t *testing.T) {
	t.Parallel()
	// Simulate a user in UTC+12 (e.g. Auckland) creating an all-day event
	// for April 15. Midnight local = April 14 12:00 UTC.
	// The exported date must be 20260415, not 20260414.
	loc := time.FixedZone("UTC+12", 12*60*60)
	events := []event.Event{{
		UID:       "allday-utcplus",
		Title:     "Auckland Day",
		StartTime: time.Date(2026, 4, 15, 0, 0, 0, 0, loc),
		EndTime:   time.Date(2026, 4, 16, 0, 0, 0, 0, loc),
		AllDay:    true,
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if !strings.Contains(ics, "DTSTART;VALUE=DATE:20260415") {
		t.Errorf("expected DTSTART date 20260415, got:\n%s", ics)
	}
	if !strings.Contains(ics, "DTEND;VALUE=DATE:20260416") {
		t.Errorf("expected DTEND date 20260416, got:\n%s", ics)
	}
}

func TestExport_AllDayEvent_StoredUTCInstantUsesLocalDate(t *testing.T) {
	prevLocal := time.Local
	time.Local = time.FixedZone("UTC+12", 12*60*60)
	t.Cleanup(func() { time.Local = prevLocal })

	// This is how a UTC-normalized all-day 2026-04-15 in UTC+12 is stored:
	// local midnight is the previous UTC date at 12:00. Export must preserve the
	// calendar date, not the UTC date of the stored instant.
	events := []event.Event{{
		UID:       "allday-stored-utc",
		Title:     "Stored Auckland Day",
		StartTime: time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		AllDay:    true,
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	ics := string(data)
	if !strings.Contains(ics, "DTSTART;VALUE=DATE:20260415") {
		t.Fatalf("expected DTSTART date 20260415, got:\n%s", ics)
	}
	if !strings.Contains(ics, "DTEND;VALUE=DATE:20260416") {
		t.Fatalf("expected DTEND date 20260416, got:\n%s", ics)
	}
}

func TestExport_MultipleExdatesRdates(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "multi-exdate",
		Title:     "Recurring",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		ExDates:   "2026-04-08T14:00:00Z,2026-04-15T14:00:00Z",
		RDates:    "2026-05-01T14:00:00Z,2026-05-08T14:00:00Z",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	ics := string(data)

	if strings.Count(ics, "EXDATE") != 2 {
		t.Errorf("expected 2 EXDATE properties, got %d\n%s", strings.Count(ics, "EXDATE"), ics)
	}
	if strings.Count(ics, "RDATE") != 2 {
		t.Errorf("expected 2 RDATE properties, got %d\n%s", strings.Count(ics, "RDATE"), ics)
	}
	if !strings.Contains(ics, "20260408") {
		t.Error("missing first EXDATE (2026-04-08)")
	}
	if !strings.Contains(ics, "20260415") {
		t.Error("missing second EXDATE (2026-04-15)")
	}
}

// Regression test for issue #421. A floating recurring component (no TZID,
// no Z on DTSTART) must emit EXDATE/RDATE as floating values too. A emit
// of them with a trail Z creates a value-type mismatch against DTSTART.
// CalDAV servers that match EXDATE against RRULE occurrences by exact string
// then fail to suppress the excluded occurrence. A deleted instance reappears.
func TestExport_FloatingExdatesRdatesNoZ(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:            "floating-exdate",
		Title:          "Recurring Floating",
		StartTime:      time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 13, 0, 0, 0, time.UTC),
		Timezone:       "FLOATING",
		RecurrenceRule: "FREQ=WEEKLY",
		Status:         "CONFIRMED",
		Transp:         "OPAQUE",
		ExDates:        "2026-04-15T12:00:00Z",
		RDates:         "2026-05-06T12:00:00Z",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	ics := string(data)

	if !strings.Contains(ics, "EXDATE:20260415T120000") {
		t.Errorf("expected floating EXDATE 20260415T120000, got:\n%s", ics)
	}
	if !strings.Contains(ics, "RDATE:20260506T120000") {
		t.Errorf("expected floating RDATE 20260506T120000, got:\n%s", ics)
	}
	if strings.Contains(ics, "20260415T120000Z") {
		t.Errorf("EXDATE must not carry a trailing Z for a floating component:\n%s", ics)
	}
	if strings.Contains(ics, "20260506T120000Z") {
		t.Errorf("RDATE must not carry a trailing Z for a floating component:\n%s", ics)
	}
}

// Regression test for issue #492. A zoned recurring component emits DTSTART
// with a TZID in local wall-clock. Its EXDATE/RDATE must then carry the same
// TZID and wall-clock value. Emit them as bare UTC (...Z) drops the TZID
// and creates a value-type mismatch against DTSTART. Servers that expand
// the RRULE in the DTSTART zone (e.g. Google) then fail to suppress the
// excluded occurrence. A deleted instance reappears.
func TestExport_ZonedExdatesRdatesCarryTZID(t *testing.T) {
	t.Parallel()
	// DTSTART 2026-04-01T09:00 America/New_York == 13:00Z (EDT, UTC-4).
	events := []event.Event{{
		UID:            "zoned-exdate",
		Title:          "Recurring Zoned",
		StartTime:      time.Date(2026, 4, 1, 13, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		Timezone:       "America/New_York",
		RecurrenceRule: "FREQ=WEEKLY",
		Status:         "CONFIRMED",
		Transp:         "OPAQUE",
		ExDates:        "2026-04-08T13:00:00Z",
		RDates:         "2026-05-06T13:00:00Z",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}}

	data, err := ExportEvents(events, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	ics := string(data)

	if !strings.Contains(ics, "EXDATE;TZID=America/New_York:20260408T090000") {
		t.Errorf("expected EXDATE;TZID=America/New_York:20260408T090000, got:\n%s", ics)
	}
	if !strings.Contains(ics, "RDATE;TZID=America/New_York:20260506T090000") {
		t.Errorf("expected RDATE;TZID=America/New_York:20260506T090000, got:\n%s", ics)
	}
	if strings.Contains(ics, "EXDATE:20260408T130000Z") {
		t.Errorf("EXDATE must not be emitted as bare UTC for a zoned component:\n%s", ics)
	}
	if strings.Contains(ics, "RDATE:20260506T130000Z") {
		t.Errorf("RDATE must not be emitted as bare UTC for a zoned component:\n%s", ics)
	}
}

func TestExport_CategoriesNotEscaped(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:        "cat-export",
		Title:      "Category Event",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:     "CONFIRMED",
		Transp:     "OPAQUE",
		Categories: "meeting,work,urgent",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	// Commas must NOT be escaped — they are value separators in CATEGORIES
	if strings.Contains(ics, `meeting\,work`) {
		t.Errorf("CATEGORIES has escaped commas:\n%s", ics)
	}
	if !strings.Contains(ics, "CATEGORIES:meeting,work,urgent") {
		t.Errorf("expected unescaped CATEGORIES, got:\n%s", ics)
	}
}

func TestExport_AttendeePartstatNotEmpty(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "partstat-test",
		Title:     "Meeting",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Attendees: []model.Attendee{
			{Email: "user@example.com", Name: "User", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if strings.Contains(ics, "PARTSTAT=;") || strings.Contains(ics, "PARTSTAT=\r") {
		t.Errorf("PARTSTAT is empty in output:\n%s", ics)
	}
	if !strings.Contains(ics, "PARTSTAT=NEEDS-ACTION") {
		t.Errorf("expected PARTSTAT=NEEDS-ACTION in output:\n%s", ics)
	}
}

func TestExport_EventWithTimezone(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "tz-export",
		Title:     "TZ Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if !strings.Contains(ics, "TZID=America/New_York") {
		t.Error("missing TZID parameter")
	}
}

func TestExport_VTimezonePresent(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "vtz-export",
		Title:     "TZ Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if !strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Fatalf("missing VTIMEZONE block:\n%s", ics)
	}
	if !strings.Contains(ics, "TZID:America/New_York") {
		t.Error("missing TZID property in VTIMEZONE")
	}
	if !strings.Contains(ics, "TZOFFSETTO:") {
		t.Error("missing TZOFFSETTO in VTIMEZONE")
	}
	if !strings.Contains(ics, "TZOFFSETFROM:") {
		t.Error("missing TZOFFSETFROM in VTIMEZONE")
	}
	// America/New_York has DST, so both STANDARD and DAYLIGHT should be present
	if !strings.Contains(ics, "BEGIN:STANDARD") {
		t.Error("missing STANDARD sub-component")
	}
	if !strings.Contains(ics, "BEGIN:DAYLIGHT") {
		t.Error("missing DAYLIGHT sub-component")
	}
}

func TestExport_VTimezoneRecurringTransitions(t *testing.T) {
	t.Parallel()
	// An event years away from "now" must still get a VTIMEZONE whose
	// transitions cover its date. Emitting recurring RRULE transition rules
	// (rather than one-shot DTSTARTs in the current year) satisfies this.
	events := []event.Event{{
		UID:       "vtz-recurring",
		Title:     "Future TZ Event",
		StartTime: time.Date(2035, 7, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2035, 7, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if !strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Fatalf("missing VTIMEZONE block:\n%s", ics)
	}
	// DST transitions must be expressed as yearly recurrence rules so the
	// VTIMEZONE applies to every year, not just the export year.
	if !strings.Contains(ics, "RRULE:FREQ=YEARLY") {
		t.Errorf("VTIMEZONE missing recurring RRULE transition:\n%s", ics)
	}
	// America/New_York: DST begins the 2nd Sunday of March, ends the 1st
	// Sunday of November.
	if !strings.Contains(ics, "FREQ=YEARLY;BYMONTH=3;BYDAY=2SU") {
		t.Errorf("missing DAYLIGHT transition rule (2nd Sunday of March):\n%s", ics)
	}
	if !strings.Contains(ics, "FREQ=YEARLY;BYMONTH=11;BYDAY=1SU") {
		t.Errorf("missing STANDARD transition rule (1st Sunday of November):\n%s", ics)
	}
}

func TestExport_VTimezoneTransitionDTSTART(t *testing.T) {
	t.Parallel()
	// RFC 5545 Section 3.6.5: a sub-component's DTSTART is the transition
	// wall-clock expressed in TZOFFSETFROM. US Eastern transitions occur at
	// 02:00 local, so both DAYLIGHT and STANDARD DTSTARTs must read T020000
	// (matching IANA-published VTIMEZONE), not the post-transition T030000.
	events := []event.Event{{
		UID:       "vtz-transition",
		Title:     "Eastern Event",
		StartTime: time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 7, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	start := strings.Index(ics, "BEGIN:VTIMEZONE")
	end := strings.Index(ics, "END:VTIMEZONE")
	if start < 0 || end < 0 {
		t.Fatalf("missing VTIMEZONE block:\n%s", ics)
	}
	vtz := ics[start:end]

	// Both US Eastern transitions occur at 02:00 local; IANA-published
	// VTIMEZONE emits DTSTART:...T020000 for each.
	if got := strings.Count(vtz, "T020000"); got != 2 {
		t.Errorf("expected two T020000 transition DTSTARTs, got %d:\n%s", got, vtz)
	}
	if want := []string{"20260308T020000", "20261101T020000"}; !strings.Contains(vtz, want[0]) || !strings.Contains(vtz, want[1]) {
		t.Errorf("VTIMEZONE missing canonical transition DTSTARTs %v:\n%s", want, vtz)
	}
	if strings.Contains(vtz, "T030000") {
		t.Errorf("VTIMEZONE DTSTART must not be the post-transition 03:00 wall-clock (T030000):\n%s", vtz)
	}
}

func TestExport_VTimezoneAnchoredOnEventYear(t *testing.T) {
	t.Parallel()
	// Issue #515: the emitted VTIMEZONE transitions must be anchored on the
	// year(s) the exported events actually fall in, not on time.Now().Year().
	// A 2018 America/New_York event must yield DTSTARTs dated in 2018 so a
	// consumer relying on the embedded VTIMEZONE (no local tzdata for the TZID)
	// resolves the correct offset for that occurrence.
	events := []event.Event{{
		UID:       "vtz-2018",
		Title:     "Past Eastern Event",
		StartTime: time.Date(2018, 7, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2018, 7, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	start := strings.Index(ics, "BEGIN:VTIMEZONE")
	end := strings.Index(ics, "END:VTIMEZONE")
	if start < 0 || end < 0 {
		t.Fatalf("missing VTIMEZONE block:\n%s", ics)
	}
	vtz := ics[start:end]

	// US Eastern 2018: DST begins 2nd Sunday of March (Mar 11), ends 1st Sunday
	// of November (Nov 4); both transitions occur at 02:00 local.
	for _, want := range []string{"20180311T020000", "20181104T020000"} {
		if !strings.Contains(vtz, want) {
			t.Errorf("VTIMEZONE missing event-year-anchored DTSTART %q:\n%s", want, vtz)
		}
	}
	// The current year's rule must not leak in when no event falls in it.
	nowYear := time.Now().Format("2006")
	if strings.Contains(vtz, "DTSTART:"+nowYear) {
		t.Errorf("VTIMEZONE leaked current-year (%s) DTSTART instead of anchoring on the event year:\n%s", nowYear, vtz)
	}
}

func TestExport_VTimezoneRuleChangeAcrossSpan(t *testing.T) {
	t.Parallel()
	// Issue #515: when the exported events span years in which the zone's DST
	// rule changed, the VTIMEZONE must carry both rule periods, with the
	// superseded (older) rule bounded by UNTIL so RFC 5545 onset resolution
	// stays unambiguous. America/New_York switched from "1st Sunday of April /
	// last Sunday of October" to "2nd Sunday of March / 1st Sunday of November"
	// in 2007 (US Energy Policy Act of 2005).
	events := []event.Event{
		{
			UID:       "vtz-2005",
			Title:     "Old-rule Event",
			StartTime: time.Date(2005, 7, 1, 14, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2005, 7, 1, 15, 0, 0, 0, time.UTC),
			Timezone:  "America/New_York",
			Status:    "CONFIRMED",
			Transp:    "OPAQUE",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			UID:       "vtz-2008",
			Title:     "New-rule Event",
			StartTime: time.Date(2008, 7, 1, 14, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2008, 7, 1, 15, 0, 0, 0, time.UTC),
			Timezone:  "America/New_York",
			Status:    "CONFIRMED",
			Transp:    "OPAQUE",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	start := strings.Index(ics, "BEGIN:VTIMEZONE")
	end := strings.Index(ics, "END:VTIMEZONE")
	if start < 0 || end < 0 {
		t.Fatalf("missing VTIMEZONE block:\n%s", ics)
	}
	vtz := ics[start:end]

	// Both rule periods present.
	for _, want := range []string{
		"FREQ=YEARLY;BYMONTH=4;BYDAY=1SU",   // old DAYLIGHT (1st Sunday April)
		"FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU", // old STANDARD (last Sunday October)
		"FREQ=YEARLY;BYMONTH=3;BYDAY=2SU",   // new DAYLIGHT (2nd Sunday March)
		"FREQ=YEARLY;BYMONTH=11;BYDAY=1SU",  // new STANDARD (1st Sunday November)
	} {
		if !strings.Contains(vtz, want) {
			t.Errorf("VTIMEZONE missing rule %q across the exported span:\n%s", want, vtz)
		}
	}
	// Superseded rules must be bounded so they do not pollute later years.
	if !strings.Contains(vtz, "UNTIL=") {
		t.Errorf("VTIMEZONE missing UNTIL bound on superseded DST rule:\n%s", vtz)
	}
	if got := strings.Count(vtz, "BEGIN:DAYLIGHT"); got != 2 {
		t.Errorf("expected two DAYLIGHT observances (old + new rule), got %d:\n%s", got, vtz)
	}
	if got := strings.Count(vtz, "BEGIN:STANDARD"); got != 2 {
		t.Errorf("expected two STANDARD observances (old + new rule), got %d:\n%s", got, vtz)
	}
}

func TestExport_VTimezoneRecurringSeriesCrossesRuleChange(t *testing.T) {
	t.Parallel()
	// Issue #518: a single recurring series contributes only its start year to
	// the VTIMEZONE span. A series that starts before a historical DST-rule
	// change but recurs past it (here America/New_York, 2005 -> the 2007 US rule
	// change) must still carry BOTH rule eras, not merely the start-year rule —
	// otherwise occurrences after the change resolve the wrong offset for
	// consumers relying on the embedded VTIMEZONE.
	events := []event.Event{
		{
			UID:            "vtz-recur-2005",
			Title:          "Long-running Event",
			StartTime:      time.Date(2005, 7, 1, 14, 0, 0, 0, time.UTC),
			EndTime:        time.Date(2005, 7, 1, 15, 0, 0, 0, time.UTC),
			Timezone:       "America/New_York",
			RecurrenceRule: "FREQ=YEARLY;UNTIL=20100701T140000Z",
			Status:         "CONFIRMED",
			Transp:         "OPAQUE",
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		},
	}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	start := strings.Index(ics, "BEGIN:VTIMEZONE")
	end := strings.Index(ics, "END:VTIMEZONE")
	if start < 0 || end < 0 {
		t.Fatalf("missing VTIMEZONE block:\n%s", ics)
	}
	vtz := ics[start:end]

	// Both rule periods present, even though only one (start-year) event exists.
	for _, want := range []string{
		"FREQ=YEARLY;BYMONTH=4;BYDAY=1SU",   // old DAYLIGHT (1st Sunday April)
		"FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU", // old STANDARD (last Sunday October)
		"FREQ=YEARLY;BYMONTH=3;BYDAY=2SU",   // new DAYLIGHT (2nd Sunday March)
		"FREQ=YEARLY;BYMONTH=11;BYDAY=1SU",  // new STANDARD (1st Sunday November)
	} {
		if !strings.Contains(vtz, want) {
			t.Errorf("VTIMEZONE missing rule %q across the recurring series horizon:\n%s", want, vtz)
		}
	}
	if !strings.Contains(vtz, "UNTIL=") {
		t.Errorf("VTIMEZONE missing UNTIL bound on superseded DST rule:\n%s", vtz)
	}
}

func TestRecurrenceEndYearBounded(t *testing.T) {
	t.Parallel()
	// Issue #520: rrule-go reports a ~290-year sentinel UNTIL when a rule
	// supplies none, so detecting "no UNTIL" via GetUntil() mis-clamped every
	// open-ended or COUNT-bounded series' VTIMEZONE span to startYear+~292. The
	// span must instead stay bounded to the current year for those rules, while a
	// real past UNTIL still clamps to its own year.
	start := time.Date(2020, 7, 1, 14, 0, 0, 0, time.UTC)
	currentYear := time.Now().Year()

	cases := []struct {
		name string
		rule string
		want int
	}{
		{"real past UNTIL clamps to its year", "FREQ=YEARLY;UNTIL=20230701T140000Z", 2023},
		{"open-ended clamps to current year", "FREQ=YEARLY", currentYear},
		{"COUNT-bounded ends at its last occurrence", "FREQ=YEARLY;COUNT=3", 2022},
		{"malformed rule degrades to start year", "FREQ=BOGUS;;;", start.Year()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := recurrenceEndYear(tc.rule, start)
			if got != tc.want {
				t.Errorf("recurrenceEndYear(%q) = %d, want %d (span must not inherit the ~290-year sentinel)", tc.rule, got, tc.want)
			}
		})
	}
}

func TestExport_VTimezoneOpenEndedSeriesBounded(t *testing.T) {
	t.Parallel()
	// Issue #520: an open-ended recurring series (no UNTIL) must clamp the
	// VTIMEZONE span to the current year rather than rrule-go's ~290-year
	// sentinel, so the month-by-month walk and emitted observances stay bounded.
	events := []event.Event{{
		UID:            "vtz-openended",
		Title:          "Unbounded Yearly Meeting",
		StartTime:      time.Date(2020, 7, 1, 14, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 7, 1, 15, 0, 0, 0, time.UTC),
		Timezone:       "America/New_York",
		RecurrenceRule: "FREQ=YEARLY",
		Status:         "CONFIRMED",
		Transp:         "OPAQUE",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	observances := strings.Count(ics, "BEGIN:STANDARD") + strings.Count(ics, "BEGIN:DAYLIGHT")
	if observances > 12 {
		t.Errorf("open-ended series bloated VTIMEZONE to %d observances; span not clamped to current year:\n%s", observances, ics)
	}
}

func TestExport_VTimezoneDSTAbolishedWithinSpan(t *testing.T) {
	t.Parallel()
	// Issue #515: Brazil abolished DST in 2019. Exporting events that straddle
	// the abolition must not leave the pre-abolition DAYLIGHT rule recurring
	// unbounded — otherwise a post-abolition occurrence resolves a spurious -02
	// summer offset from the embedded VTIMEZONE instead of standing -03.
	mk := func(uid string, year int) event.Event {
		return event.Event{
			UID:       uid,
			Title:     "Brazil Event",
			StartTime: time.Date(year, 1, 15, 14, 0, 0, 0, time.UTC),
			EndTime:   time.Date(year, 1, 15, 15, 0, 0, 0, time.UTC),
			Timezone:  "America/Sao_Paulo",
			Status:    "CONFIRMED",
			Transp:    "OPAQUE",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
	}

	data, _ := ExportEvents([]event.Event{mk("br-2017", 2017), mk("br-2021", 2021)}, "")
	ics := string(data)

	// Every DAYLIGHT observance must be bounded once DST no longer applies at
	// the end of the span.
	foundDaylight := false
	for _, p := range strings.Split(ics, "BEGIN:DAYLIGHT")[1:] {
		blk := p
		if i := strings.Index(p, "END:DAYLIGHT"); i >= 0 {
			blk = p[:i]
		}
		if !strings.Contains(blk, "RRULE") {
			continue
		}
		foundDaylight = true
		if !strings.Contains(blk, "UNTIL=") {
			t.Errorf("unbounded DAYLIGHT rule survives DST abolition:\n%s", blk)
		}
	}
	if !foundDaylight {
		t.Fatalf("expected pre-abolition DAYLIGHT observances:\n%s", ics)
	}
}

func TestExport_VTimezoneNoDST(t *testing.T) {
	t.Parallel()
	// Asia/Kolkata does not observe DST
	events := []event.Event{{
		UID:       "vtz-nodst",
		Title:     "No DST Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Timezone:  "Asia/Kolkata",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if !strings.Contains(ics, "TZID:Asia/Kolkata") {
		t.Error("missing TZID for Asia/Kolkata")
	}
	if !strings.Contains(ics, "BEGIN:STANDARD") {
		t.Error("missing STANDARD sub-component")
	}
	if strings.Contains(ics, "BEGIN:DAYLIGHT") {
		t.Error("Asia/Kolkata should not have DAYLIGHT sub-component")
	}
	if !strings.Contains(ics, "+0530") {
		t.Error("expected +0530 offset for Asia/Kolkata")
	}
}

func TestExport_NoVTimezoneWithoutTZID(t *testing.T) {
	t.Parallel()
	// Events without a timezone should not generate VTIMEZONE
	events := []event.Event{{
		UID:       "no-vtz",
		Title:     "UTC Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)

	if strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Error("UTC event should not generate VTIMEZONE")
	}
}

func TestExport_EventWithAlarms(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "alarm-export",
		Title:     "Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 min"},
			{Action: "EMAIL", TriggerValue: "-PT1H", Description: "1 hour"},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if strings.Count(ics, "BEGIN:VALARM") != 2 {
		t.Errorf("expected 2 VALARMs, got %d", strings.Count(ics, "BEGIN:VALARM"))
	}
	if !strings.Contains(ics, "ACTION:DISPLAY") {
		t.Error("missing ACTION:DISPLAY")
	}
}

// TestExport_AlarmRepeatWithoutDuration guards RFC 5545 §3.8.6.2: REPEAT
// MUST be paired with DURATION. A Repeat with no Duration must not emit a
// bare REPEAT, which strict CalDAV servers (e.g. Google) reject with 400.
func TestExport_AlarmRepeatWithoutDuration(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "alarm-repeat-no-duration",
		Title:     "Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "no interval", Repeat: 3},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if strings.Contains(ics, "REPEAT") {
		t.Errorf("emitted REPEAT without DURATION (non-conformant per RFC 5545 §3.8.6.2):\n%s", ics)
	}
}

// TestExport_AlarmRepeatWithDuration confirms the conformant pair still
// round-trips when both REPEAT and DURATION are present.
func TestExport_AlarmRepeatWithDuration(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "alarm-repeat-with-duration",
		Title:     "Alarm Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "repeat", Duration: "PT5M", Repeat: 3},
		},
	}}

	data, _ := ExportEvents(events, "")
	ics := string(data)
	if !strings.Contains(ics, "REPEAT:3") {
		t.Errorf("missing REPEAT:3 when DURATION present:\n%s", ics)
	}
	if !strings.Contains(ics, "DURATION:PT5M") {
		t.Errorf("missing DURATION:PT5M:\n%s", ics)
	}
}
