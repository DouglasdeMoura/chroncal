package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
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

// TestExportResource_LegacyRowsOfTrashedOwnerRefused: a calendar whose rows
// of a UID are live legacy overrides, while the one master of the UID is
// in the trash of another calendar, exports nothing. The series in the
// trash owns the rows, so an orphan export would put the occurrence on the
// server twice after the restore. An edit of the row marks no calendar.
func TestExportResource_LegacyRowsOfTrashedOwnerRefused(t *testing.T) {
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
	dirty := func(calendarID int64) int64 {
		t.Helper()
		var n int64
		if err := engine.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_resources WHERE calendar_id = ? AND uid = ? AND dirty = 1`, calendarID, master.UID).Scan(&n); err != nil {
			t.Fatalf("count dirty rows: %v", err)
		}
		return n
	}
	clearDirty := func() {
		t.Helper()
		if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET dirty = 0 WHERE uid = ?`, master.UID); err != nil {
			t.Fatalf("clear dirty: %v", err)
		}
	}
	edit := func(title string) {
		t.Helper()
		if _, err := engine.events.Update(ctx, legacy.ID, event.UpdateParams{
			CalendarID: calB, Title: title, StartTime: legacy.StartTime, EndTime: legacy.EndTime,
		}); err != nil {
			t.Fatalf("Update(legacy, %s): %v", title, err)
		}
	}
	// A live owner gets the mark of an edit of its legacy row.
	clearDirty()
	edit("Legacy edited")
	if a, b := dirty(calA), dirty(calB); a != 1 || b != 0 {
		t.Fatalf("dirty rows with a live owner = A %d, B %d; want A 1, B 0", a, b)
	}
	if err := engine.events.DeleteSeries(ctx, calA, master.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if err := engine.events.RestoreByID(ctx, legacy.ID); err != nil {
		t.Fatalf("RestoreByID(legacy): %v", err)
	}
	// An owner in the trash gets no mark, and no other calendar gets it.
	clearDirty()
	edit("Legacy late")
	if a, b := dirty(calA), dirty(calB); a != 0 || b != 0 {
		t.Errorf("dirty rows with the owner in the trash = A %d, B %d; want 0 and 0", a, b)
	}
	if _, err := engine.exportResource(ctx, calB, "event", master.UID); !errors.Is(err, errResourceMissing) {
		t.Errorf("exportResource(B) err = %v, want errResourceMissing (the owner is in the trash)", err)
	}
}

// TestPush_OwnedElsewhereKeepsServerObject: a calendar that owns nothing
// of a UID, because the series lives in another calendar, keeps its
// server object. The push clears the dirty flag only. A copy that an old
// whole-series upload made stays as a known leftover for sync doctor: a
// delete could also hit a real single-instance object of that calendar.
func TestPush_OwnedElsewhereKeepsServerObject(t *testing.T) {
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
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || len(res.Errors) != 0 {
		t.Fatalf("push B = %+v, %v", res, err)
	}
	srv.mu.Lock()
	_, kept := srv.objects[path]
	srv.mu.Unlock()
	if !kept {
		t.Fatalf("the server object %s of B went, want it kept", path)
	}
	n, err := engine.q.CountTombstonesByCalendarAndUID(ctx, storage.CountTombstonesByCalendarAndUIDParams{CalendarID: calB, Uid: master.UID})
	if err != nil || n != 0 {
		t.Fatalf("tombstones of B after the push = %d, %v; want 0", n, err)
	}
	dirty, err := engine.q.ListDirtySyncResources(ctx, calB)
	if err != nil || len(dirty) != 0 {
		t.Fatalf("dirty rows of B after the push = %d, %v; want 0", len(dirty), err)
	}
}

// TestExportResource_LiveRowBesideTrashedMaster: a calendar whose master
// is in the trash, while another calendar holds a live master, exports a
// live override beside the trashed master (a restore by ID) as an orphan
// instance, so the row reaches the server.
func TestExportResource_LiveRowBesideTrashedMaster_Refused(t *testing.T) {
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
	if _, err := engine.exportResource(ctx, calB, "event", masterA.UID); !errors.Is(err, errResourceMissing) {
		t.Fatalf("exportResource(B) err = %v, want errResourceMissing (the master of B is in the trash)", err)
	}
}

// TestPush_LiveRowBesideTrashedMasterKeepsServerSeries: a dirty UID whose
// calendar holds its own master in the trash and a live override does not
// PUT the override alone. The server object of the series stays.
func TestPush_LiveRowBesideTrashedMasterKeepsServerSeries(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
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
	path := "/cal/b/" + masterA.UID + ".ics"
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + masterA.UID +
		"\r\nDTSTART:20260518T090000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\nSUMMARY:Series\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	srv.mu.Lock()
	srv.objects[path] = body
	srv.mu.Unlock()
	if _, err := engine.db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy, remote_url) VALUES (?, ?, 'event', 1, 'sync-token', ?)
		 ON CONFLICT DO UPDATE SET dirty = 1, remote_url = excluded.remote_url`,
		calB, masterA.UID, path); err != nil {
		t.Fatalf("seed sync row: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || len(res.Errors) != 0 {
		t.Fatalf("push B = %+v, %v", res, err)
	}
	srv.mu.Lock()
	got := srv.objects[path]
	srv.mu.Unlock()
	if got != body {
		t.Errorf("server object after the push = %q, want the series body unchanged", got)
	}
}

// TestPush_MissingResourceLocalAnomalyClearsOnly: a dirty sync row with a
// remote URL and no local row at all (a purge that outran its tombstone)
// is a local anomaly. The push clears the dirty flag and writes no
// tombstone, so the server object stays.
func TestPush_MissingResourceLocalAnomalyClearsOnly(t *testing.T) {
	t.Parallel()
	engine, _, srv, _, calB := newICloudPairEngine(t)
	ctx := context.Background()
	const uid = "anomaly-uid"
	path := "/cal/b/" + uid + ".ics"
	srv.mu.Lock()
	srv.objects[path] = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTART:20260518T090000Z\r\nSUMMARY:Kept\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	srv.mu.Unlock()
	if _, err := engine.db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy, remote_url) VALUES (?, ?, 'event', 1, 'sync-token', ?)`,
		calB, uid, path); err != nil {
		t.Fatalf("seed sync row: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || len(res.Errors) != 0 {
		t.Fatalf("push B = %+v, %v", res, err)
	}
	n, err := engine.q.CountTombstonesByCalendarAndUID(ctx, storage.CountTombstonesByCalendarAndUIDParams{CalendarID: calB, Uid: uid})
	if err != nil || n != 0 {
		t.Fatalf("tombstones after the push = %d, %v; want 0", n, err)
	}
	srv.mu.Lock()
	_, kept := srv.objects[path]
	srv.mu.Unlock()
	if !kept {
		t.Fatalf("the server object %s went for a local anomaly", path)
	}
	dirty, err := engine.q.ListDirtySyncResources(ctx, calB)
	if err != nil || len(dirty) != 0 {
		t.Fatalf("dirty rows after the push = %d, %v; want 0", len(dirty), err)
	}
}

// TestPush_LegacyRowsBesideTwoLiveMastersKeepServerObject: B holds live
// legacy rows of a UID with live masters in A and C. No series owns the
// rows of B, so the push of B clears the dirty flag and keeps the server
// object of B. A tombstone would make the next pull trash the live rows.
func TestPush_LegacyRowsBesideTwoLiveMastersKeepServerObject(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	res, err := engine.db.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Third')`)
	if err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	calC, _ := res.LastInsertId()
	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	for _, cal := range []int64{calA, calC} {
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
			UID: "split-uid", CalendarID: cal, Title: "Standup",
			StartTime: start, EndTime: start.Add(30 * time.Minute), RecurrenceRule: "FREQ=DAILY;COUNT=5",
		}); err != nil {
			t.Fatalf("upsert master %d: %v", cal, err)
		}
	}
	legacy, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "split-uid", CalendarID: calB, Title: "Legacy late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy row: %v", err)
	}
	path := "/cal/b/split-uid.ics"
	srv.mu.Lock()
	srv.objects[path] = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:split-uid\r\nRECURRENCE-ID:20260519T090000Z\r\nDTSTART:20260519T100000Z\r\nSUMMARY:Legacy late\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	srv.mu.Unlock()
	if _, err := engine.db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy, remote_url) VALUES (?, 'split-uid', 'event', 1, 'sync-token', ?)
		 ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, remote_url = excluded.remote_url`,
		calB, path); err != nil {
		t.Fatalf("seed sync row: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || len(res.Errors) != 0 {
		t.Fatalf("push B = %+v, %v", res, err)
	}
	srv.mu.Lock()
	_, kept := srv.objects[path]
	srv.mu.Unlock()
	if !kept {
		t.Fatal("the server object of B went, want it kept")
	}
	if r, err := engine.events.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy row after the push = %+v, %v; want it live", r, err)
	}
}

// TestPush_OwnedElsewhereInAnotherAccountKeepsServerObject: calendar B of
// a second account holds a legacy row of a series that lives in A. The
// server object of B is a real resource of that account, not a copy of an
// old whole-series upload, so the push of B writes no tombstone.
func TestPush_OwnedElsewhereInAnotherAccountKeepsServerObject(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	second, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "second", ServerUrl: srv.srv.URL, AuthType: "basic", Username: "user2",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	engine.credStore.(*mockCredStore).creds[second.ID] = auth.Credential{AccountID: second.ID, Username: "user2", Password: "pw"}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: calB, AccountID: &second.ID, RemoteUrl: storage.StringToNullable("/cal/b/"),
	}); err != nil {
		t.Fatalf("relink B: %v", err)
	}
	master := createSeries(t, engine, calA)
	at := master.StartTime.Add(24 * time.Hour)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: calB, Title: "Instance",
		StartTime: at.Add(time.Hour), EndTime: at.Add(2 * time.Hour), RecurrenceID: at.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert legacy row: %v", err)
	}
	path := "/cal/b/" + master.UID + ".ics"
	srv.mu.Lock()
	srv.objects[path] = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + master.UID + "\r\nRECURRENCE-ID:20260519T090000Z\r\nDTSTART:20260519T100000Z\r\nSUMMARY:Instance\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	srv.mu.Unlock()
	if _, err := engine.db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy, remote_url) VALUES (?, ?, 'event', 1, 'sync-token', ?)
		 ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, remote_url = excluded.remote_url`,
		calB, master.UID, path); err != nil {
		t.Fatalf("seed sync row: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || len(res.Errors) != 0 {
		t.Fatalf("push B = %+v, %v", res, err)
	}
	srv.mu.Lock()
	_, kept := srv.objects[path]
	srv.mu.Unlock()
	if !kept {
		t.Fatal("the server object of B in the second account went, want it kept")
	}
}

// TestExportResource_OneVEVENTPerRecurrenceID: the export of a series
// with its own override and a legacy override of the same occurrence
// holds one VEVENT for that RECURRENCE-ID, the own row.
func TestExportResource_OneVEVENTPerRecurrenceID(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	master, _, inst := createSeriesWithOverride(t, engine, calA)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: calB, Title: "Legacy twin",
		StartTime: inst, EndTime: inst.Add(time.Hour), RecurrenceID: inst.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert legacy twin: %v", err)
	}
	data, err := engine.exportResource(ctx, calA, "event", master.UID)
	if err != nil {
		t.Fatalf("exportResource(A): %v", err)
	}
	body := string(data)
	if n := strings.Count(body, "RECURRENCE-ID"); n != 1 {
		t.Errorf("export of A holds %d RECURRENCE-IDs, want 1:\n%s", n, body)
	}
	if strings.Contains(body, "Legacy twin") || !strings.Contains(body, "SUMMARY:Late") {
		t.Errorf("export of A = want the own override, not the legacy twin:\n%s", body)
	}
}
