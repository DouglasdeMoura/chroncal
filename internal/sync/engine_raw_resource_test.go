package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

func TestImportFetchedResourceUsesRawCalendarDataWithoutPRODID(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	calendars, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}

	const raw = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:icloud-no-prodid\r\n" +
		"DTSTAMP:20260829T080000Z\r\n" +
		"DTSTART:20260829T090000Z\r\n" +
		"DTEND:20260829T100000Z\r\n" +
		"SUMMARY:iCloud payload\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	parsed, err := ical.NewDecoder(strings.NewReader(raw)).Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	uid, imported, _, err := engine.importFetchedResource(ctx, calendars[0].ID, nil, fetchedResource{
		path: "/calendar/icloud-no-prodid.ics",
		href: "/calendar/icloud-no-prodid.ics",
		etag: "etag-icloud",
		data: parsed,
		raw:  raw,
	})
	if err != nil {
		t.Fatalf("importFetchedResource: %v", err)
	}
	if uid != "icloud-no-prodid" || !imported {
		t.Fatalf("uid = %q, imported = %t; want icloud-no-prodid, true", uid, imported)
	}
	if _, err := engine.events.GetByUID(ctx, uid); err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
}
