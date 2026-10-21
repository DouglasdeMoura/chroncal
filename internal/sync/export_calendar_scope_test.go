package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestExportResource_ScopedToCalendar stores one series with the same UID
// in two calendars. The export of one calendar must hold the master and
// the override of that calendar only (issue #842). Before the fix, the
// push of calendar B could upload the series of calendar A.
func TestExportResource_ScopedToCalendar(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	for _, c := range []struct {
		id    int64
		title string
	}{{calA, "Standup A"}, {calB, "Standup B"}} {
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
			UID: "shared-series", CalendarID: c.id, Title: c.title,
			StartTime: start, EndTime: start.Add(30 * time.Minute),
			RecurrenceRule: "FREQ=DAILY;COUNT=5",
		}); err != nil {
			t.Fatalf("upsert master %q: %v", c.title, err)
		}
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
			UID: "shared-series", CalendarID: c.id, Title: c.title + " (late)",
			StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
			RecurrenceID: second.Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("upsert override %q: %v", c.title, err)
		}
	}

	for _, c := range []struct {
		id         int64
		want, deny string
	}{{calB, "Standup B", "Standup A"}, {calA, "Standup A", "Standup B"}} {
		data, err := engine.exportResource(ctx, c.id, "event", "shared-series")
		if err != nil {
			t.Fatalf("exportResource(calendar %d): %v", c.id, err)
		}
		body := string(data)
		if n := strings.Count(body, "BEGIN:VEVENT"); n != 2 {
			t.Errorf("export of calendar %d holds %d VEVENTs, want 2 (master and override):\n%s", c.id, n, body)
		}
		if !strings.Contains(body, "SUMMARY:"+c.want) {
			t.Errorf("export of calendar %d lacks %q:\n%s", c.id, c.want, body)
		}
		if strings.Contains(body, c.deny) {
			t.Errorf("export of calendar %d holds %q of the other calendar:\n%s", c.id, c.deny, body)
		}
	}

	// The conflict record and the organizer gate read the same master.
	idB, err := engine.lookupOwnerID(ctx, calB, "event", "shared-series")
	if err != nil {
		t.Fatalf("lookupOwnerID(B): %v", err)
	}
	masterB, err := engine.events.GetByCalendarUID(ctx, calB, "shared-series")
	if err != nil {
		t.Fatalf("GetByCalendarUID(B): %v", err)
	}
	if idB != masterB.ID {
		t.Errorf("lookupOwnerID(B) = %d, want the master of calendar B %d", idB, masterB.ID)
	}
}

// TestExportResource_LegacyOverrideCalendarHasNoResource: a calendar that
// holds legacy overrides of a UID only, while a live master of the UID is
// in another calendar, exports no resource. A PUT of the overrides alone
// would store an object with no master.
func TestExportResource_LegacyOverrideCalendarHasNoResource(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	res, err := engine.db.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Legacy')`)
	if err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	calC, _ := res.LastInsertId()

	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "legacy-series", CalendarID: calA, Title: "Standup A",
		StartTime: start, EndTime: start.Add(30 * time.Minute), RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}); err != nil {
		t.Fatalf("upsert master A: %v", err)
	}
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "legacy-series", CalendarID: calC, Title: "Legacy late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
		RecurrenceID: second.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}

	// One live master: the export of A carries the legacy override, and
	// the export of C has no resource.
	data, err := engine.exportResource(ctx, calA, "event", "legacy-series")
	if err != nil {
		t.Fatalf("exportResource(A): %v", err)
	}
	if !strings.Contains(string(data), "SUMMARY:Legacy late") {
		t.Errorf("export of A lacks the legacy override:\n%s", data)
	}
	if _, err := engine.exportResource(ctx, calC, "event", "legacy-series"); !errors.Is(err, errResourceMissing) {
		t.Errorf("exportResource(C) err = %v, want errResourceMissing", err)
	}

	// Two live masters: the export of C still has no resource.
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "legacy-series", CalendarID: calB, Title: "Standup B",
		StartTime: start, EndTime: start.Add(30 * time.Minute), RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}); err != nil {
		t.Fatalf("upsert master B: %v", err)
	}
	if _, err := engine.exportResource(ctx, calC, "event", "legacy-series"); !errors.Is(err, errResourceMissing) {
		t.Errorf("exportResource(C) with two masters err = %v, want errResourceMissing", err)
	}
	// With two live masters the legacy row belongs to no series
	// (uidmove.LegacyOwner), so no export carries it.
	for _, cal := range []int64{calA, calB} {
		data, err := engine.exportResource(ctx, cal, "event", "legacy-series")
		if err != nil {
			t.Fatalf("exportResource(%d) with two masters: %v", cal, err)
		}
		if strings.Contains(string(data), "Legacy late") {
			t.Errorf("export of calendar %d with two masters carries the legacy override:\n%s", cal, data)
		}
	}
}

// TestExportResource_OrphanRowsBesideTrashedMaster: a calendar whose rows
// of a UID are live legacy overrides, while the one master of the UID is
// in the trash of another calendar, exports those rows as orphan
// instances. A row that a restore by ID brought back then reaches the
// server.
func TestExportResource_OrphanRowsBesideTrashedMaster(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "orphan-series", CalendarID: calA, Title: "Standup A",
		StartTime: start, EndTime: start.Add(30 * time.Minute), RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("upsert master A: %v", err)
	}
	legacy, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "orphan-series", CalendarID: calB, Title: "Legacy late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
		RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := engine.events.DeleteSeries(ctx, calA, master.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if err := engine.events.RestoreByID(ctx, legacy.ID); err != nil {
		t.Fatalf("RestoreByID(legacy): %v", err)
	}
	data, err := engine.exportResource(ctx, calB, "event", master.UID)
	if err != nil {
		t.Fatalf("exportResource(B): %v", err)
	}
	if !strings.Contains(string(data), "SUMMARY:Legacy late") {
		t.Errorf("export of B lacks the restored orphan row:\n%s", data)
	}
}

// TestPush_MissingResourceWithRemoteURLQueuesTombstone: a calendar that
// owns nothing of a UID, but whose sync row names a server object (a
// duplicate from an old whole-series upload), gets a tombstone for that
// object instead of a silent clear of the dirty flag.
func TestPush_MissingResourceWithRemoteURLQueuesTombstone(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	master := createSeries(t, engine, calA)
	second := master.StartTime.Add(24 * time.Hour)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: calB, Title: "Legacy late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
		RecurrenceID: second.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 {
		t.Fatalf("push A = %+v, %v", res, err)
	}
	// The duplicate server object of calendar B and its sync row.
	path := "/cal/b/" + master.UID + ".ics"
	srv.mu.Lock()
	srv.objects[path] = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + master.UID + "\r\nDTSTART:20260518T090000Z\r\nSUMMARY:Duplicate\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	srv.mu.Unlock()
	if _, err := engine.db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy, remote_url) VALUES (?, ?, 'event', 1, 'sync-token', ?)`,
		calB, master.UID, path); err != nil {
		t.Fatalf("seed sync row: %v", err)
	}
	// The push pass writes the tombstone and then sends its DELETE, so
	// the server object of B is gone after one pass.
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || len(res.Errors) != 0 {
		t.Fatalf("push B = %+v, %v", res, err)
	}
	srv.mu.Lock()
	_, stillThere := srv.objects[path]
	srv.mu.Unlock()
	if stillThere {
		t.Fatalf("the duplicate server object %s survived the push of B", path)
	}
	n, err := engine.q.CountTombstonesByCalendarAndUID(ctx, storage.CountTombstonesByCalendarAndUIDParams{CalendarID: calB, Uid: master.UID})
	if err != nil || n != 0 {
		t.Fatalf("tombstones of B after the push = %d, %v; want 0 (sent)", n, err)
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/a/"+master.UID+".ics")
}

// TestExportResource_LiveRowBesideTrashedMaster: a calendar whose master
// is in the trash, while another calendar holds a live master, exports a
// live override beside the trashed master (a restore by ID) as an orphan
// instance, so the row reaches the server.
func TestExportResource_LiveRowBesideTrashedMaster(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	masterA := createSeries(t, engine, calA)
	masterB, overrideB, _ := createSeriesWithOverride(t, engine, calB)
	sameUID(t, engine, masterB.ID, masterA.UID)
	sameUID(t, engine, overrideB.ID, masterA.UID)
	if err := engine.events.DeleteSeries(ctx, calB, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	if err := engine.events.RestoreByID(ctx, overrideB.ID); err != nil {
		t.Fatalf("RestoreByID(override B): %v", err)
	}
	data, err := engine.exportResource(ctx, calB, "event", masterA.UID)
	if err != nil {
		t.Fatalf("exportResource(B): %v", err)
	}
	if !strings.Contains(string(data), "SUMMARY:Late") || strings.Contains(string(data), "RRULE") {
		t.Errorf("export of B = want the live override as an orphan instance and no master:\n%s", data)
	}
}
