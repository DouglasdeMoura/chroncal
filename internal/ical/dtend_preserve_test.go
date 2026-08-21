package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// exchangeDTENDIcal is the shape Exchange emits: DTSTART parses, DTEND
// carries a TZID no tz database resolves. The event must still appear
// locally, and the remote path must keep the original string.
const exchangeDTENDIcal = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:dtend-preserve@example.com
DTSTAMP:20260101T000000Z
DTSTART:20260101T150000Z
DTEND;TZID=Customized Time Zone:20260101T163000
SUMMARY:Exchange meeting
END:VEVENT
END:VCALENDAR
`

func importOneEvent(t *testing.T, data string) event.Event {
	t.Helper()
	result, err := ImportFileRemote(strings.NewReader(data))
	if err != nil {
		t.Fatalf("ImportFileRemote: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	return result.Events[0]
}

func TestImportRemotePreservesUnparseableDTEND(t *testing.T) {
	t.Parallel()
	e := importOneEvent(t, exchangeDTENDIcal)

	if !e.EndTime.After(e.StartTime) {
		t.Fatalf("fabricated span missing: start=%v end=%v", e.StartTime, e.EndTime)
	}
	var preserved *model.XProperty
	for i := range e.XProperties {
		if e.XProperties[i].Name == xpropOriginalDTEND {
			preserved = &e.XProperties[i]
		}
	}
	if preserved == nil {
		t.Fatalf("no %s x-property in %+v", xpropOriginalDTEND, e.XProperties)
	}
	if preserved.Value != "20260101T163000" {
		t.Errorf("preserved value = %q, want the raw server string", preserved.Value)
	}
	if !strings.Contains(preserved.Params, "TZID") || !strings.Contains(preserved.Params, "Customized Time Zone") {
		t.Errorf("preserved params = %q, want the TZID param", preserved.Params)
	}
}

func TestImportFileDoesNotPreserveDTEND(t *testing.T) {
	t.Parallel()
	result, err := ImportFile(strings.NewReader(exchangeDTENDIcal))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	for _, xp := range e.XProperties {
		if xp.Name == xpropOriginalDTEND {
			t.Errorf("file import must not set %s", xpropOriginalDTEND)
		}
	}
}

func TestExportPrefersPreservedDTEND(t *testing.T) {
	t.Parallel()
	e := importOneEvent(t, exchangeDTENDIcal)
	data, err := ExportEvents([]event.Event{e}, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "DTEND;TZID=Customized Time Zone:20260101T163000") {
		t.Errorf("export lost the server's original DTEND:\n%s", out)
	}
	if strings.Contains(out, xpropOriginalDTEND) {
		t.Errorf("export leaked the preservation slot as an x-property:\n%s", out)
	}
}

// A normal event keeps its computed DTEND: the preservation slot only fires
// when import stored it.
func TestExportNormalEventKeepsComputedDTEND(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "normal-dtend@example.com",
		Title:     "Normal",
		Timezone:  "America/New_York",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	}}
	data, err := ExportEvents(events, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	out := string(data)
	if strings.Contains(out, xpropOriginalDTEND) {
		t.Errorf("normal event carries the preservation slot:\n%s", out)
	}
	if !strings.Contains(out, "DTEND") {
		t.Errorf("normal event lost its DTEND:\n%s", out)
	}
}
