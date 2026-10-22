package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
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

// TestEventDeleteSeries_NumericLegacyIDTakesTheCalendar: a series delete
// with --calendar and the ID of a legacy override targets the series of the
// --calendar calendar, not the calendar of the row.
func TestEventDeleteSeries_NumericLegacyIDTakesTheCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	_, legacyID := seedLegacyOverrideInHome(t)
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
	got, err := eventResource.resolveIn(ctx, a, strconv.FormatInt(legacyID, 10), "Work", true)
	if err != nil {
		t.Fatalf("resolveIn(legacy ID, Work): %v", err)
	}
	if got.CalendarID != work {
		t.Errorf("target calendar = %d, want Work %d", got.CalendarID, work)
	}
}

// TestEventDeleteSeries_OwnTrashedMasterRefusedBeforePrompt: a calendar with
// its own master in the trash and a live override beside it holds no series
// to delete. The resolver refuses it with the trash message before the
// prompt, as the service would after it.
func TestEventDeleteSeries_OwnTrashedMasterRefusedBeforePrompt(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	ctx := context.Background()
	home, err := resolveCalendarID(ctx, a, "Home")
	if err != nil {
		t.Fatalf("resolve Home: %v", err)
	}
	at := time.Date(2026, 4, 7, 9, 0, 0, 0, time.UTC)
	override, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: uid, CalendarID: home, Title: "Home late",
		StartTime: at.Add(time.Hour), EndTime: at.Add(2 * time.Hour), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert Home override: %v", err)
	}
	if err := a.Events.DeleteSeries(ctx, home, uid); err != nil {
		t.Fatalf("DeleteSeries(Home): %v", err)
	}
	if err := a.Events.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID(override): %v", err)
	}
	_, err = eventResource.resolveIn(ctx, a, uid, "Home", true)
	if err == nil || !strings.Contains(err.Error(), "is in the trash in calendar") {
		t.Fatalf("resolveIn(uid, Home) err = %v, want the trash message before the prompt", err)
	}
	if _, serr := a.Events.DeleteSeriesWithUndo(ctx, home, uid); !errors.Is(serr, event.ErrSeriesInTrash) {
		t.Errorf("DeleteSeries(Home) err = %v, want ErrSeriesInTrash (the same rule)", serr)
	}
}

// importOrphan imports one orphan instance of uid into calendar.
func importOrphan(t *testing.T, uid, calendar, summary string) {
	t.Helper()
	ics := filepath.Join(t.TempDir(), "orphan.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T100000Z\r\nDTEND:20260407T103000Z\r\n" +
		"SUMMARY:" + summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", calendar); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
}

// TestResolveEventOccurrence_OrphanByCalendar: --calendar picks one of two
// orphan rows, and that calendar names the row. An orphan row in another
// calendar than --calendar stops the edit with a hint that names its
// calendar, so the edit is not a move.
func TestResolveEventOccurrence_OrphanByCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	for _, name := range []string{"Work", "Home", "Other"} {
		if _, _, err := runChroncalCommand(t, "calendar", "create", name); err != nil {
			t.Fatalf("calendar create %s: %v", name, err)
		}
	}
	const twin, single = "orphan-twin", "orphan-single-cal"
	importOrphan(t, twin, "Work", "Twin Work")
	importOrphan(t, twin, "Home", "Twin Home")
	importOrphan(t, single, "Work", "Single")
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	ctx := context.Background()
	home, err := resolveCalendarID(ctx, a, "Home")
	if err != nil {
		t.Fatalf("resolve Home: %v", err)
	}
	other, err := resolveCalendarID(ctx, a, "Other")
	if err != nil {
		t.Fatalf("resolve Other: %v", err)
	}
	const rid = "2026-04-07T09:00:00Z"
	e, _, viaCalendar, err := resolveEventOccurrence(ctx, a, twin, rid, home, "Home", true)
	if err != nil || e.CalendarID != home || !viaCalendar || e.Title != "Twin Home" {
		t.Fatalf("resolve twin in Home = %q calendar %d via %v, %v; want the Home row via the calendar", e.Title, e.CalendarID, viaCalendar, err)
	}
	_, _, _, err = resolveEventOccurrence(ctx, a, single, rid, other, "Other", true)
	if err == nil || !strings.Contains(err.Error(), `the row is in calendar "Work"`) {
		t.Errorf("resolve single in Other err = %v, want a hint that names Work", err)
	}
}

// TestResolveEventOccurrence_CalendarWithTrashedMasterStops: --calendar on a
// calendar whose own master is in the trash stops the edit of an occurrence
// there. The lookup does not fall back to the series of another calendar.
func TestResolveEventOccurrence_CalendarWithTrashedMasterStops(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	ctx := context.Background()
	home, err := resolveCalendarID(ctx, a, "Home")
	if err != nil {
		t.Fatalf("resolve Home: %v", err)
	}
	at := time.Date(2026, 4, 7, 9, 0, 0, 0, time.UTC)
	override, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: uid, CalendarID: home, Title: "Home late",
		StartTime: at.Add(time.Hour), EndTime: at.Add(2 * time.Hour), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert Home override: %v", err)
	}
	if err := a.Events.DeleteSeries(ctx, home, uid); err != nil {
		t.Fatalf("DeleteSeries(Home): %v", err)
	}
	if err := a.Events.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID(override): %v", err)
	}
	_, _, _, err = resolveEventOccurrence(ctx, a, uid, at.Format(time.RFC3339), home, "Home", true)
	if err == nil || !strings.Contains(err.Error(), "restore the series first") {
		t.Errorf("resolveEventOccurrence(Home) err = %v, want the series-in-trash stop", err)
	}
}

// TestEventDeleteSeries_OrphanUIDWithoutCalendar: `event delete <uid>
// --series` with no --calendar takes the service rule. A UID with orphan
// rows in one calendar and no master gets the delete.
func TestEventDeleteSeries_OrphanUIDWithoutCalendar(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create Work: %v", err)
	}
	const uid = "orphan-series-no-calendar"
	importOrphan(t, uid, "Work", "Orphan")
	if stdout, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--series", "--yes"); err != nil {
		t.Fatalf("event delete --series: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if _, stderr, err := runChroncalCommand(t, "event", "get", uid, "--recurrence-id", "2026-04-07T09:00:00Z"); err == nil {
		t.Errorf("the orphan row is live after the series delete (stderr=%q)", stderr)
	}
}

// TestAmbiguousUIDHint_PerCommand: the hint for a UID in two calendars names
// --calendar only where the command uses it to choose the series: event
// delete, and event update with --recurrence-id.
func TestAmbiguousUIDHint_PerCommand(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	for _, tc := range []struct {
		args         []string
		wantCalendar bool
	}{
		{[]string{"event", "get", uid}, false},
		{[]string{"event", "rsvp", uid, "--status", "accepted"}, false},
		{[]string{"event", "update", uid, "--title", "X"}, false},
		{[]string{"event", "update", uid, "--recurrence-id", "2026-04-07T09:00:00Z", "--title", "X"}, true},
		{[]string{"event", "delete", uid, "--yes"}, true},
	} {
		_, stderr, err := runChroncalCommand(t, tc.args...)
		if err == nil || !strings.Contains(stderr, "more than one calendar") {
			t.Errorf("%v: err = %v, stderr = %q; want the ambiguity refusal", tc.args, err, stderr)
			continue
		}
		if got := strings.Contains(stderr, "--calendar"); got != tc.wantCalendar {
			t.Errorf("%v: hint names --calendar = %v, want %v (stderr=%q)", tc.args, got, tc.wantCalendar, stderr)
		}
	}
	for _, cal := range []string{"Work", "Home"} {
		if _, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--series", "--calendar", cal, "--yes"); err != nil {
			t.Fatalf("series delete in %s: %v (stderr=%q)", cal, err, stderr)
		}
	}
	_, stderr, err := runChroncalCommand(t, "event", "restore", uid)
	if err == nil || strings.Contains(stderr, "--calendar") {
		t.Errorf("event restore: err = %v, stderr = %q; want a refusal with no --calendar hint", err, stderr)
	}
}
