package freebusy

import (
	"strings"
	"testing"
	"time"
)

func TestExport_WritesVFreeBusyCalendar(t *testing.T) {
	t.Parallel()

	data, err := Export(Result{
		UID:       "fb-1",
		DTStamp:   time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC),
		Start:     time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC),
		Organizer: "mailto:owner@example.com",
		Periods: []Period{
			{
				Start: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
				End:   time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
				Type:  Busy,
			},
			{
				Start: time.Date(2026, 4, 10, 11, 0, 0, 0, time.UTC),
				End:   time.Date(2026, 4, 10, 11, 30, 0, 0, time.UTC),
				Type:  BusyTentative,
			},
		},
	}, "Work")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	text := string(data)
	for _, want := range []string{
		"BEGIN:VCALENDAR",
		"BEGIN:VFREEBUSY",
		"UID:fb-1",
		"ORGANIZER:mailto:owner@example.com",
		"FREEBUSY:20260410T090000Z/20260410T100000Z",
		"FREEBUSY;FBTYPE=BUSY-TENTATIVE:20260410T110000Z/20260410T113000Z",
		"END:VFREEBUSY",
		"END:VCALENDAR",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("export missing %q:\n%s", want, text)
		}
	}
}
