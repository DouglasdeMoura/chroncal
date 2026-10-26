package ical

import (
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

const sameContentBase = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//x//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:weekly\r\nDTSTAMP:20260403T100000Z\r\nSEQUENCE:0\r\n" +
	"DTSTART:20260403T100000Z\r\nDTEND:20260403T110000Z\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Standup\r\n" +
	"CATEGORIES:Work,Team\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:Soon\r\nEND:VALARM\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT5M\r\nDESCRIPTION:Now\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:weekly\r\nDTSTAMP:20260403T100000Z\r\nRECURRENCE-ID:20260410T100000Z\r\n" +
	"DTSTART:20260410T120000Z\r\nDTEND:20260410T130000Z\r\nSUMMARY:Moved standup\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func decodeForTest(t *testing.T, body string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(body)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cal
}

// TestSameContent covers the rule of issue #918: the fields that a store
// or a server writes on its own are no content.
func TestSameContent(t *testing.T) {
	// The server copy has another DTSTAMP, SEQUENCE, CREATED, and
	// LAST-MODIFIED, a STATUS that the store writes by default, alarm
	// UIDs, another order of the alarms and of the categories, and the
	// override before the master.
	server := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//server//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:weekly\r\nDTSTAMP:20261020T080000Z\r\nRECURRENCE-ID:20260410T100000Z\r\n" +
		"DTSTART:20260410T120000Z\r\nDTEND:20260410T130000Z\r\nSUMMARY:Moved standup\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:weekly\r\nDTSTAMP:20261020T080000Z\r\nSEQUENCE:4\r\n" +
		"CREATED:20260101T000000Z\r\nLAST-MODIFIED:20261020T080000Z\r\nSTATUS:CONFIRMED\r\n" +
		"DTSTART:20260403T100000Z\r\nDTEND:20260403T110000Z\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Standup\r\n" +
		"CATEGORIES:Team,Work\r\n" +
		"BEGIN:VALARM\r\nUID:a2\r\nACTION:DISPLAY\r\nTRIGGER:-PT5M\r\nDESCRIPTION:Now\r\nEND:VALARM\r\n" +
		"BEGIN:VALARM\r\nUID:a1\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:Soon\r\nEND:VALARM\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	if !SameContent([]byte(sameContentBase), decodeForTest(t, server)) {
		t.Error("SameContent = false for two copies of one series")
	}

	for name, change := range map[string][2]string{
		"summary":       {"SUMMARY:Standup\r\n", "SUMMARY:Standup!\r\n"},
		"alarm trigger": {"TRIGGER:-PT5M", "TRIGGER:-PT6M"},
		"override":      {"SUMMARY:Moved standup", "SUMMARY:Moved again"},
		"rule":          {"RRULE:FREQ=WEEKLY", "RRULE:FREQ=DAILY"},
	} {
		other := strings.Replace(sameContentBase, change[0], change[1], 1)
		if SameContent([]byte(sameContentBase), decodeForTest(t, other)) {
			t.Errorf("%s: SameContent = true for a changed copy", name)
		}
	}

	if SameContent([]byte("not a calendar"), decodeForTest(t, sameContentBase)) {
		t.Error("SameContent = true for a body that does not parse")
	}
	if SameContent([]byte(sameContentBase), nil) {
		t.Error("SameContent = true with no server calendar")
	}
}

// TestSameContentKeepsTheServerCalendar checks that the comparison does
// not change the server calendar, which the caller keeps.
func TestSameContentKeepsTheServerCalendar(t *testing.T) {
	body := strings.Replace(sameContentBase, "DTSTART:20260403T100000Z", "DTSTART;TZID=Eastern Standard Time:20260403T100000", 1)
	cal := decodeForTest(t, body)
	SameContent([]byte(sameContentBase), cal)
	var b strings.Builder
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(b.String(), "TZID=Eastern Standard Time") {
		t.Errorf("the comparison changed the server calendar:\n%s", b.String())
	}
}
