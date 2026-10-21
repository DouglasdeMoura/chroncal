package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestCheckRowInCalendar_AcceptsEachOwnedLegacyRow: --calendar on the
// calendar of the master accepts each legacy override that the series
// owns, also the row that loses the tie rule at its occurrence. A calendar
// that owns no series of the row refuses it.
func TestCheckRowInCalendar_AcceptsEachOwnedLegacyRow(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid, _ := seedLegacyOverrideInHome(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Other"); err != nil {
		t.Fatalf("calendar create Other: %v", err)
	}
	ics := filepath.Join(t.TempDir(), "legacy2.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T110000Z\r\nDTEND:20260407T113000Z\r\n" +
		"SUMMARY:Legacy other\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Other"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	ctx := context.Background()
	rows, err := a.Queries.ListEventsByUIDAndRecurrenceID(ctx, storage.ListEventsByUIDAndRecurrenceIDParams{Uid: uid, RecurrenceID: "2026-04-07T09:00:00Z"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("legacy rows = %d, %v; want 2", len(rows), err)
	}
	work, err := resolveCalendarID(ctx, a, "Work")
	if err != nil {
		t.Fatalf("resolve Work: %v", err)
	}
	other, err := resolveCalendarID(ctx, a, "Other")
	if err != nil {
		t.Fatalf("resolve Other: %v", err)
	}
	for _, r := range rows {
		e, err := a.Events.Get(ctx, r.ID)
		if err != nil {
			t.Fatalf("get row %d: %v", r.ID, err)
		}
		if err := checkRowInCalendar(ctx, a, e, work, "Work"); err != nil {
			t.Errorf("checkRowInCalendar(row %d, Work) = %v, want nil", r.ID, err)
		}
		if e.CalendarID != other {
			if err := checkRowInCalendar(ctx, a, e, other, "Other"); err == nil {
				t.Errorf("checkRowInCalendar(row %d, Other) = nil, want a refusal", r.ID)
			}
		}
	}
}

// TestEventDeleteHooks_PushTheMasterCalendar: the delete hooks of the event
// resource return the calendar that the delete wrote. A plain delete of a
// legacy override and a series delete through the legacy calendar both
// write the calendar of the master.
func TestEventDeleteHooks_PushTheMasterCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid, legacyID := seedLegacyOverrideInHome(t)
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	ctx := context.Background()
	work, err := resolveCalendarID(ctx, a, "Work")
	if err != nil {
		t.Fatalf("resolve Work: %v", err)
	}
	home, err := resolveCalendarID(ctx, a, "Home")
	if err != nil {
		t.Fatalf("resolve Home: %v", err)
	}
	got, err := eventResource.del(ctx, a, row{ID: legacyID, UID: uid, CalendarID: home})
	if err != nil || got != work {
		t.Fatalf("del(legacy row) = calendar %d, %v; want Work %d", got, err, work)
	}
	if err := a.Events.RestoreByID(ctx, legacyID); err != nil {
		t.Fatalf("RestoreByID(legacy row): %v", err)
	}
	got, err = eventResource.delSeries(ctx, a, row{UID: uid, CalendarID: home})
	if err != nil || got != work {
		t.Errorf("delSeries(through Home) = calendar %d, %v; want Work %d", got, err, work)
	}
}
