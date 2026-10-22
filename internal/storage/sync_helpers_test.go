package storage

import (
	"context"
	"fmt"
	"testing"
)

func TestMarkResourceDirty_NoopWhenNoSyncResource(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Should be a no-op when no sync_resources row exists (no account linked)
	err = MarkResourceDirty(context.Background(), db, 1, "non-existent-uid", "event")
	if err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}
}

func TestMarkResourceDirty_ReturnsCalendarLookupError(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	err = MarkResourceDirty(context.Background(), db, 1, "uid", "event")
	if err == nil {
		t.Fatal("MarkResourceDirty err = nil, want database error")
	}
}

func TestMarkResourceDirty_SkipsZeroCalendarOrEmptyUID(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := MarkResourceDirty(ctx, db, 0, "uid", "event"); err != nil {
		t.Errorf("should skip when calendarID=0: %v", err)
	}
	if err := MarkResourceDirty(ctx, db, 1, "", "event"); err != nil {
		t.Errorf("should skip when uid is empty: %v", err)
	}
}

func TestCreateTombstoneIfSynced_NoopWhenNotSynced(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	created, err := CreateTombstoneIfSynced(context.Background(), db, 1, "no-sync-resource")
	if err != nil {
		t.Fatalf("CreateTombstoneIfSynced: %v", err)
	}
	if created {
		t.Error("should not create tombstone when no sync resource exists")
	}
}

func TestCreateTombstoneIfSynced_ReturnsSyncResourceLookupError(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	created, err := CreateTombstoneIfSynced(context.Background(), db, 1, "uid")
	if err == nil {
		t.Fatal("CreateTombstoneIfSynced err = nil, want database error")
	}
	if created {
		t.Fatal("CreateTombstoneIfSynced created = true, want false on database error")
	}
}

func TestCreateTombstoneIfSynced_SkipsZeroCalendarOrEmptyUID(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	created, err := CreateTombstoneIfSynced(ctx, db, 0, "uid")
	if err != nil || created {
		t.Errorf("should skip when calendarID=0: created=%v err=%v", created, err)
	}
	created, err = CreateTombstoneIfSynced(ctx, db, 1, "")
	if err != nil || created {
		t.Errorf("should skip when uid is empty: created=%v err=%v", created, err)
	}
}

func TestCreateTombstoneIfSynced_CreatesTombstone(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	// Create a sync resource with a remote URL
	err = q.UpsertSyncResource(ctx, UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          "tombstone-test-uid",
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/event.ics",
		Etag:         "etag-123",
		Dirty:        0,
		SyncStrategy: "sync-token",
	})
	if err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	created, err := CreateTombstoneIfSynced(ctx, db, calID, "tombstone-test-uid")
	if err != nil {
		t.Fatalf("CreateTombstoneIfSynced: %v", err)
	}
	if !created {
		t.Error("should have created tombstone for synced resource")
	}

	// Verify tombstone exists
	tombstones, err := q.ListTombstonesByCalendar(ctx, calID)
	if err != nil {
		t.Fatalf("ListTombstones: %v", err)
	}
	if len(tombstones) != 1 {
		t.Fatalf("expected 1 tombstone, got %d", len(tombstones))
	}
	if tombstones[0].Uid != "tombstone-test-uid" {
		t.Errorf("tombstone uid = %q, want %q", tombstones[0].Uid, "tombstone-test-uid")
	}
}

func TestCreateTombstoneIfSynced_DedupesRepeatedDeletes(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	err = q.UpsertSyncResource(ctx, UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          "dup-tombstone-uid",
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/event.ics",
		Etag:         "etag-123",
		Dirty:        0,
		SyncStrategy: "sync-token",
	})
	if err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// Delete the synced resource twice (e.g. delete, restore via sync, delete
	// again before a sync flush). Both calls must succeed and collapse onto a
	// single tombstone row for the (calendar_id, uid) pair.
	for i := 0; i < 2; i++ {
		created, err := CreateTombstoneIfSynced(ctx, db, calID, "dup-tombstone-uid")
		if err != nil {
			t.Fatalf("CreateTombstoneIfSynced (call %d): %v", i, err)
		}
		if !created {
			t.Fatalf("CreateTombstoneIfSynced (call %d) created = false, want true", i)
		}
	}

	tombstones, err := q.ListTombstonesByCalendar(ctx, calID)
	if err != nil {
		t.Fatalf("ListTombstones: %v", err)
	}
	if len(tombstones) != 1 {
		t.Fatalf("expected 1 tombstone after repeated deletes, got %d", len(tombstones))
	}
}

func TestMarkResourceDirty_SetsDirtyFlag(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	// Link calendar to an account so MarkResourceDirty acts
	account, err := q.CreateAccount(ctx, CreateAccountParams{
		Name: "test", ServerUrl: "https://example.com", AuthType: "basic", Username: "u",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_ = q.LinkCalendarToAccount(ctx, LinkCalendarToAccountParams{
		AccountID: &account.ID, RemoteUrl: strPtr("https://example.com/cal/"), ID: calID,
	})

	// Mark dirty — should upsert a sync_resource row
	err = MarkResourceDirty(ctx, db, calID, "dirty-test-uid", "event")
	if err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}

	// Verify it's dirty
	dirty, err := q.ListDirtySyncResources(ctx, calID)
	if err != nil {
		t.Fatalf("ListDirty: %v", err)
	}
	if len(dirty) != 1 {
		t.Fatalf("expected 1 dirty resource, got %d", len(dirty))
	}
	if dirty[0].Uid != "dirty-test-uid" {
		t.Errorf("dirty uid = %q, want %q", dirty[0].Uid, "dirty-test-uid")
	}
}

func strPtr(s string) *string { return &s }

// TestMoveBackRetargetsTombstonesOnlyForALiveCopy moves the rows of a UID
// back from C to D twice. A tombstone of X names C each time. With a live
// row of the UID in D, the tombstone must name D. Without one, it must
// become a plain delete.
func TestMoveBackRetargetsTombstonesOnlyForALiveCopy(t *testing.T) {
	t.Parallel()
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (1, 'acct', 'https://example.com', 'basic', 'u')`,
		`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (10, 'X', 1, '/x/'), (11, 'C', 1, '/c/'), (12, 'D', 1, '/d/')`,
		`INSERT INTO events (calendar_id, uid, title, start_time, end_time) VALUES (12, 'live-uid', 'In D', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	target := int64(11)
	for _, uid := range []string{"live-uid", "gone-uid"} {
		if err := q.CreateTombstone(ctx, CreateTombstoneParams{CalendarID: 10, Uid: uid, RemoteUrl: "/x/" + uid + ".ics", MovedToCalendarID: &target}); err != nil {
			t.Fatalf("CreateTombstone %s: %v", uid, err)
		}
		live := func(ctx context.Context) (int64, error) {
			return q.CountLiveEventRowsInCalendar(ctx, CountLiveEventRowsInCalendarParams{CalendarID: 12, Uid: uid})
		}
		if err := RecordResourceMoveBack(ctx, q, 11, 12, uid, func(context.Context) error { return nil }, live); err != nil {
			t.Fatalf("RecordResourceMoveBack %s: %v", uid, err)
		}
	}
	live, err := q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: 10, Uid: "live-uid"})
	if err != nil || live.MovedToCalendarID == nil || *live.MovedToCalendarID != 12 {
		t.Fatalf("tombstone with a live copy in D = %+v, %v; want the target D", live, err)
	}
	gone, err := q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: 10, Uid: "gone-uid"})
	if err != nil || gone.MovedToCalendarID != nil {
		t.Fatalf("tombstone without a copy in D = %+v, %v; want a plain delete", gone, err)
	}
}

// TestApplyImportNeedsThePrepareToken calls ApplyImport with a zero token.
// It must refuse: the rules before the upsert never ran.
func TestApplyImportNeedsThePrepareToken(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ApplyImport(context.Background(), q, PreparedImport{}); err == nil {
		t.Fatal("ApplyImport with a zero token succeeded")
	}
}

// TestHoldingCalendarsOfATombstone checks the open move conflicts that
// hold the source of a move. A conflict on the move target does not count.
// A conflict on the excluded calendar does not count, also for a tombstone
// without a move target. A conflict on a calendar that an account delete
// left unlinked does not count.
func TestHoldingCalendarsOfATombstone(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (1, 'acct', 'https://example.com', 'basic', 'u')`,
		`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (10, 'A', 1, '/a/'), (11, 'B', 1, '/b/'), (12, 'C', 1, '/c/')`,
		`INSERT INTO tombstones (id, calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (1, 10, 'uid', '/a/uid.ics', 11)`,
		`INSERT INTO tombstones (id, calendar_id, uid, remote_url) VALUES (2, 10, 'plain', '/a/plain.ics')`,
		`INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag, move_source_calendar_id, move_state) VALUES
			(11, 'event', 1, 'uid', 'l', 's', 'e', 10, 'open'),
			(11, 'event', 2, 'plain', 'l', 's', 'e', 10, 'open')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	holders := func(id, exclude int64) []string {
		t.Helper()
		rows, err := q.ListTombstoneHoldingCalendars(ctx, ListTombstoneHoldingCalendarsParams{ID: id, ExcludeCalendarID: exclude})
		if err != nil {
			t.Fatalf("ListTombstoneHoldingCalendars: %v", err)
		}
		var names []string
		for _, r := range rows {
			names = append(names, r.Name)
		}
		return names
	}
	if got := holders(1, 0); len(got) != 0 {
		t.Fatalf("holders with a conflict on the target = %v, want none", got)
	}
	if got := holders(2, 11); len(got) != 0 {
		t.Fatalf("holders of a plain tombstone, the pushed calendar excluded = %v, want none", got)
	}
	if got := holders(2, 0); len(got) != 1 || got[0] != "B" {
		t.Fatalf("holders of a plain tombstone = %v, want B", got)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag, move_source_calendar_id, move_state) VALUES (12, 'event', 1, 'uid', 'l', 's', 'e', 10, 'open')`); err != nil {
		t.Fatalf("conflict on C: %v", err)
	}
	if got := holders(1, 11); len(got) != 1 || got[0] != "C" {
		t.Fatalf("holders with a conflict on C = %v, want C", got)
	}
	if _, err := db.ExecContext(ctx, `UPDATE calendars SET account_id = NULL WHERE id = 12`); err != nil {
		t.Fatalf("unlink C: %v", err)
	}
	if got := holders(1, 11); len(got) != 0 {
		t.Fatalf("holders after the unlink of C = %v, want none", got)
	}
}

// TestTombstoneWriteResetsSourceGone checks the source_gone mark. The mark
// does not change the version, and a new write of the tombstone resets it:
// the new write can name a new server object.
func TestTombstoneWriteResetsSourceGone(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO calendars (id, name) VALUES (10, 'A')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := q.CreateTombstone(ctx, CreateTombstoneParams{CalendarID: 10, Uid: "uid", RemoteUrl: "/a/uid.ics"}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: 10, Uid: "uid"})
	if err != nil {
		t.Fatalf("GetTombstone: %v", err)
	}
	if n, err := q.MarkTombstoneSourceGone(ctx, MarkTombstoneSourceGoneParams{ID: ts.ID, Version: ts.Version + 1}); err != nil || n != 0 {
		t.Fatalf("mark at a stale version = %d, %v; want no row", n, err)
	}
	if n, err := q.MarkTombstoneSourceGone(ctx, MarkTombstoneSourceGoneParams{ID: ts.ID, Version: ts.Version}); err != nil || n != 1 {
		t.Fatalf("mark = %d, %v; want one row", n, err)
	}
	marked, err := q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: 10, Uid: "uid"})
	if err != nil || marked.SourceGone != 1 || marked.Version != ts.Version {
		t.Fatalf("marked tombstone = %+v, %v; want source_gone at the same version", marked, err)
	}
	if err := q.CreateTombstone(ctx, CreateTombstoneParams{CalendarID: 10, Uid: "uid", RemoteUrl: "/a/uid.ics"}); err != nil {
		t.Fatalf("CreateTombstone again: %v", err)
	}
	again, err := q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: 10, Uid: "uid"})
	if err != nil || again.SourceGone != 0 {
		t.Fatalf("tombstone after a new write = %+v, %v; want source_gone reset", again, err)
	}
}

// TestWaitListDerivesTheCause checks the list of waits. A row shows only
// while the cause of its wait holds, and no path clears the columns:
//   - B waits on the hold by C, and the gate then clears its dirty flag.
//   - A waits on the hold by C for its source delete, and a restore then
//     clears its tombstone.
//   - B2 waits on a hold by C, and the moot step then closes that conflict.
//   - D waits for the source delete in A, and the delete then goes.
func TestWaitListDerivesTheCause(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (1, 'acct', 'https://example.com', 'basic', 'u')`,
		`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (10, 'A', 1, '/a/'), (11, 'B', 1, '/b/'), (12, 'C', 1, '/c/'), (13, 'D', 1, '/d/')`,
		`INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (10, 'held', '/a/held.ics', 11), (10, 'moot', '/a/moot.ics', 11), (10, 'free', '/a/free.ics', 13)`,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy, wait_kind, wait_holder_calendar_id) VALUES
			(11, 'held', 'event', '', '', 1, 'sync-token', 1, 12),
			(10, 'held', 'event', '/a/held.ics', 'e', 0, 'sync-token', 1, 12),
			(11, 'moot', 'event', '', '', 1, 'sync-token', 1, 12),
			(13, 'free', 'event', '', '', 1, 'sync-token', 2, 0)`,
		`INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag, move_source_calendar_id, move_state) VALUES
			(12, 'event', 1, 'held', 'l', 's', 'e', 10, 'open'),
			(12, 'event', 2, 'moot', 'l', 's', 'e', 10, 'open')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	list := func() []string {
		t.Helper()
		rows, err := q.ListWaitingSyncResources(ctx)
		if err != nil {
			t.Fatalf("ListWaitingSyncResources: %v", err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, fmt.Sprintf("%d/%s", r.CalendarID, r.Uid))
		}
		return out
	}
	want := func(step string, rows ...string) {
		t.Helper()
		if got := list(); fmt.Sprint(got) != fmt.Sprint(rows) {
			t.Fatalf("waits after %s = %v, want %v", step, got, rows)
		}
	}
	want("the seed", "10/held", "11/held", "11/moot", "13/free")
	for _, step := range []struct{ name, stmt string }{
		{"the gate clear of B", `UPDATE sync_resources SET dirty = 0 WHERE calendar_id = 11 AND uid = 'held'`},
		{"the restore in A", `DELETE FROM tombstones WHERE calendar_id = 10 AND uid = 'held'`},
		{"the moot close", `UPDATE sync_conflicts SET move_state = 'abandoned', resolved_at = '2026-04-01T00:00:00Z' WHERE uid = 'moot'`},
		{"the source delete", `DELETE FROM tombstones WHERE uid = 'free'`},
	} {
		if _, err := db.ExecContext(ctx, step.stmt); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		switch step.name {
		case "the gate clear of B":
			want(step.name, "10/held", "11/moot", "13/free")
		case "the restore in A":
			want(step.name, "11/moot", "13/free")
		case "the moot close":
			want(step.name, "13/free")
		default:
			want(step.name)
		}
	}
}

// TestPushResultsClearTheWait checks that a wait ends with the next push. A
// failure replaces the wait, and a success clears it. A wait itself does
// not change push_fail_count.
func TestPushResultsClearTheWait(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO calendars (id, name) VALUES (10, 'A')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := q.UpsertSyncResource(ctx, UpsertSyncResourceParams{CalendarID: 10, Uid: "uid", OwnerType: "event", Dirty: 1, SyncStrategy: "sync-token"}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	wait := func() {
		t.Helper()
		if err := q.SetSyncResourceWait(ctx, SetSyncResourceWaitParams{WaitKind: 2, WaitHolderCalendarID: 0, CalendarID: 10, Uid: "uid"}); err != nil {
			t.Fatalf("SetSyncResourceWait: %v", err)
		}
	}
	row := func() SyncResource {
		t.Helper()
		sr, err := q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: 10, Uid: "uid"})
		if err != nil {
			t.Fatalf("GetSyncResource: %v", err)
		}
		return sr
	}
	wait()
	wait()
	if sr := row(); sr.WaitKind != 2 || sr.PushFailCount != 0 {
		t.Fatalf("row after two waits = %+v; want the wait with no failure", sr)
	}
	if err := q.RecordSyncResourcePushFailure(ctx, RecordSyncResourcePushFailureParams{LastPushError: "boom", CalendarID: 10, Uid: "uid"}); err != nil {
		t.Fatalf("RecordSyncResourcePushFailure: %v", err)
	}
	if sr := row(); sr.WaitKind != 0 || sr.PushFailCount != 1 {
		t.Fatalf("row after a failure = %+v; want the wait replaced", sr)
	}
	wait()
	if err := q.ClearSyncResourcePushFailure(ctx, ClearSyncResourcePushFailureParams{CalendarID: 10, Uid: "uid"}); err != nil {
		t.Fatalf("ClearSyncResourcePushFailure: %v", err)
	}
	if sr := row(); sr.WaitKind != 0 || sr.PushFailCount != 0 {
		t.Fatalf("row after a success = %+v; want no wait", sr)
	}
}

// TestRestoreClearOfAGoneSourceMarksTheRowDirty checks the tombstone clear
// of a restore. A tombstone with the source_gone mark makes the row drop
// its href and its etag. The row becomes dirty with a new rev, in the same
// transaction as the tombstone delete. A plain tombstone leaves the row as
// it is.
func TestRestoreClearOfAGoneSourceMarksTheRowDirty(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO calendars (id, name) VALUES (10, 'A')`,
		`INSERT INTO tombstones (calendar_id, uid, remote_url, source_gone) VALUES (10, 'gone', '/a/gone.ics', 1), (10, 'plain', '/a/plain.ics', 0)`,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy, rev) VALUES
			(10, 'gone', 'event', '/a/gone.ics', 'e1', 0, 'sync-token', 4),
			(10, 'plain', 'event', '/a/plain.ics', 'e2', 0, 'sync-token', 4)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	for _, uid := range []string{"gone", "plain"} {
		if err := ClearTombstonesOfUIDInTx(ctx, db, q, 10, uid); err != nil {
			t.Fatalf("ClearTombstonesOfUIDInTx %s: %v", uid, err)
		}
		if _, err := q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: 10, Uid: uid}); err == nil {
			t.Fatalf("tombstone of %s survived the clear", uid)
		}
	}
	gone, err := q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: 10, Uid: "gone"})
	if err != nil || gone.RemoteUrl != "" || gone.Etag != "" || gone.Dirty != 1 || gone.Rev != 5 {
		t.Fatalf("row of the gone source = %+v, %v; want a dirty row with no href and a new rev", gone, err)
	}
	plain, err := q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: 10, Uid: "plain"})
	if err != nil || plain.RemoteUrl != "/a/plain.ics" || plain.Dirty != 0 || plain.Rev != 4 {
		t.Fatalf("row of the plain tombstone = %+v, %v; want it unchanged", plain, err)
	}
}

// TestHeldWaitNeedsTheConflictOfItsSource checks the kind-1 wait. An open
// move conflict of the holder calendar that names another source does not
// hold the row. The source of the conflict must hold the tombstone that the
// row waits on.
func TestHeldWaitNeedsTheConflictOfItsSource(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (1, 'acct', 'https://example.com', 'basic', 'u')`,
		`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (10, 'A', 1, '/a/'), (11, 'B', 1, '/b/'), (12, 'C', 1, '/c/'), (13, 'D', 1, '/d/')`,
		`INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (10, 'uid', '/a/uid.ics', 11)`,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy, wait_kind, wait_holder_calendar_id) VALUES
			(11, 'uid', 'event', '', '', 1, 'sync-token', 1, 12)`,
		`INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag, move_source_calendar_id, move_state) VALUES (12, 'event', 1, 'uid', 'l', 's', 'e', 13, 'open')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if rows, err := q.ListWaitingSyncResources(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("waits with a conflict of another source = %v, %v; want none", rows, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sync_conflicts SET move_source_calendar_id = 10`); err != nil {
		t.Fatalf("name A: %v", err)
	}
	rows, err := q.ListWaitingSyncResources(ctx)
	if err != nil || len(rows) != 1 || rows[0].HolderName != "C" {
		t.Fatalf("waits with the conflict of A = %v, %v; want the wait on C", rows, err)
	}
}

// TestClearMoveTargetOfTombstoneIsGuarded checks the guard of the clear of
// one move target. A stale version or another target is a miss with no
// write. The read state clears the target and bumps the version.
func TestClearMoveTargetOfTombstoneIsGuarded(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO calendars (id, name) VALUES (10, 'A'), (11, 'B'), (12, 'C')`,
		`INSERT INTO tombstones (id, calendar_id, uid, remote_url, moved_to_calendar_id, version) VALUES (1, 10, 'uid', '/a/uid.ics', 11, 3)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	b, c := int64(11), int64(12)
	for _, tc := range []struct {
		name    string
		target  int64
		version int64
		want    int64
	}{{"stale version", b, 2, 0}, {"other target", c, 3, 0}, {"read state", b, 3, 1}} {
		target := tc.target
		n, err := q.ClearMoveTargetOfTombstone(ctx, ClearMoveTargetOfTombstoneParams{ID: 1, MovedToCalendarID: &target, Version: tc.version})
		if err != nil || n != tc.want {
			t.Fatalf("%s: rows = %d, %v; want %d", tc.name, n, err, tc.want)
		}
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: 10, Uid: "uid"})
	if err != nil || ts.MovedToCalendarID != nil || ts.Version != 4 {
		t.Fatalf("tombstone = %+v, %v; want a plain delete at version 4", ts, err)
	}
}

// TestMovedCopyWithoutASyncRowIsNotLive checks the view live_move_copies. A
// copy in a linked calendar with live rows but no sync row never pushes, so
// it is not live. Its sync row makes it live. A tombstone of the UID in
// that calendar makes it gone again.
func TestMovedCopyWithoutASyncRowIsNotLive(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (1, 'acct', 'https://example.com', 'basic', 'u')`,
		`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (11, 'B', 1, '/b/')`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class) VALUES ('uid', 11, 'Copy', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z', 'CONFIRMED', 'OPAQUE', 'PUBLIC')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	exists := func() int64 {
		t.Helper()
		n, err := q.MovedCopyExists(ctx, MovedCopyExistsParams{CalendarID: 11, Uid: "uid", OwnerType: "event"})
		if err != nil {
			t.Fatalf("MovedCopyExists: %v", err)
		}
		return n
	}
	if n := exists(); n != 0 {
		t.Fatalf("MovedCopyExists without a sync row = %d, want not live", n)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy) VALUES (11, 'uid', 'event', 1, 'sync-token')`); err != nil {
		t.Fatalf("sync row: %v", err)
	}
	if n := exists(); n == 0 {
		t.Fatal("MovedCopyExists with a sync row = 0, want live")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (11, 'uid', '/b/uid.ics')`); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if n := exists(); n != 0 {
		t.Fatalf("MovedCopyExists with a tombstone = %d, want gone", n)
	}
}

// TestMarkMovedCopiesDirtyGivesACopyItsRow checks the mark of moved copies
// after a link or a reset. A live copy that a move tombstone names gets a
// dirty row. A live copy of an open move conflict gets one too. An item
// that no move names gets none, and an existing row stays as it is.
func TestMarkMovedCopiesDirtyGivesACopyItsRow(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (1, 'acct', 'https://example.com', 'basic', 'u')`,
		`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (10, 'A', 1, '/a/'), (11, 'B', 1, '/b/')`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class) VALUES
			('moved', 11, 'Moved', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z', 'CONFIRMED', 'OPAQUE', 'PUBLIC'),
			('held', 11, 'Held', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z', 'CONFIRMED', 'OPAQUE', 'PUBLIC'),
			('plain', 11, 'Plain', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z', 'CONFIRMED', 'OPAQUE', 'PUBLIC'),
			('kept', 11, 'Kept', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z', 'CONFIRMED', 'OPAQUE', 'PUBLIC')`,
		`INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (10, 'moved', '/a/moved.ics', 11), (10, 'kept', '/a/kept.ics', 11)`,
		`INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag, move_source_calendar_id, move_state) VALUES (11, 'event', 2, 'held', 'l', 's', 'e', 10, 'open')`,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy, rev) VALUES (11, 'kept', 'event', '/b/kept.ics', 'e1', 0, 'sync-token', 3)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if err := q.MarkMovedCopiesDirtyInCalendar(ctx, 11); err != nil {
		t.Fatalf("MarkMovedCopiesDirtyInCalendar: %v", err)
	}
	for _, uid := range []string{"moved", "held"} {
		if sr, err := q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: 11, Uid: uid}); err != nil || sr.Dirty != 1 || sr.RemoteUrl != "" {
			t.Fatalf("row of %s = %+v, %v; want a dirty row", uid, sr, err)
		}
	}
	if _, err := q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: 11, Uid: "plain"}); err == nil {
		t.Fatal("an item that no move names got a row, want none")
	}
	if sr, err := q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: 11, Uid: "kept"}); err != nil || sr.Dirty != 0 || sr.Rev != 3 || sr.RemoteUrl != "/b/kept.ics" {
		t.Fatalf("existing row = %+v, %v; want it unchanged", sr, err)
	}
}
