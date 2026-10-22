package storage_test

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestMarkResourceDirtyKeepsTrashedSeriesInItsCalendar holds one UID in A and
// B. A holds a master and an override, both in the trash, as a delete of the
// series leaves them. B holds a live master. A mark of A goes to no calendar.
// A master in the trash still owns its calendar, so the override of A is not
// a legacy override (see uidmove). The rows of A stay local until the master
// comes back. A legacy override in C, a calendar with no master row, sends
// its mark to B.
func TestMarkResourceDirtyKeepsTrashedSeriesInItsCalendar(t *testing.T) {
	t.Parallel()
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cal := func(name string) int64 {
		t.Helper()
		c, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
		if err != nil {
			t.Fatalf("CreateCalendar %s: %v", name, err)
		}
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID: c.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/" + name + "/"),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount %s: %v", name, err)
		}
		return c.ID
	}
	calA, calB, calC := cal("a"), cal("b"), cal("c")
	row := func(calendarID int64, recurrenceID string, deleted bool) {
		t.Helper()
		var deletedAt any
		if deleted {
			deletedAt = "2026-05-01T00:00:00Z"
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id, deleted_at)
			 VALUES ('u1', ?, 't', '2026-05-18T09:00:00Z', '2026-05-18T10:00:00Z', ?, ?)`,
			calendarID, recurrenceID, deletedAt); err != nil {
			t.Fatalf("insert row: %v", err)
		}
	}
	row(calA, "", true)
	row(calA, "2026-05-19T09:00:00Z", true)
	row(calB, "", false)
	row(calC, "2026-05-20T09:00:00Z", false)

	dirty := func(calendarID int64) bool {
		t.Helper()
		sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "u1"})
		return err == nil && sr.Dirty == 1
	}
	if err := storage.MarkResourceDirty(ctx, db, calA, "u1", "event"); err != nil {
		t.Fatalf("MarkResourceDirty A: %v", err)
	}
	if dirty(calA) || dirty(calB) {
		t.Fatalf("after a mark of A: A dirty = %v, B dirty = %v; want neither", dirty(calA), dirty(calB))
	}
	if err := storage.MarkResourceDirty(ctx, db, calC, "u1", "event"); err != nil {
		t.Fatalf("MarkResourceDirty C: %v", err)
	}
	if !dirty(calB) || dirty(calC) {
		t.Fatalf("after a mark of C: B dirty = %v, C dirty = %v; want B", dirty(calB), dirty(calC))
	}
}

// TestMarkResourceDirtyRecordsAnUnownedEdit: A and B hold a live master of one
// UID, and C holds a legacy override of it. No series owns the override, so
// no export holds it. A mark of C marks no calendar dirty. It sets
// edit_unowned on the sync row of C. The edit stays local, and the sync
// status lists it. The mark has its own column: the real push failures of
// the row survive the mark and its clear. After the delete of the master of
// B, A owns the override: the mark goes to A, and edit_unowned of C clears.
func TestMarkResourceDirtyRecordsAnUnownedEdit(t *testing.T) {
	t.Parallel()
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cal := func(name string) int64 {
		t.Helper()
		c, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
		if err != nil {
			t.Fatalf("CreateCalendar %s: %v", name, err)
		}
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID: c.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/" + name + "/"),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount %s: %v", name, err)
		}
		return c.ID
	}
	calA, calB, calC := cal("a"), cal("b"), cal("c")
	for _, r := range []struct {
		cal   int64
		recID string
	}{{calA, ""}, {calB, ""}, {calC, "2026-05-20T09:00:00Z"}} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id)
			 VALUES ('u1', ?, 't', '2026-05-18T09:00:00Z', '2026-05-18T10:00:00Z', ?)`, r.cal, r.recID); err != nil {
			t.Fatalf("insert row: %v", err)
		}
	}
	// A real push failure of the sync row of C, from before the edit.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, push_fail_count, last_push_error)
		 VALUES (?, 'u1', 'event', '/c/u1.ics', 3, 'HTTP 500')`, calC); err != nil {
		t.Fatalf("seed a push failure: %v", err)
	}
	state := func(calendarID int64) storage.SyncResource {
		t.Helper()
		sr, _ := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "u1"})
		return sr
	}
	wantFailure := func(when string) {
		t.Helper()
		if sr := state(calC); sr.PushFailCount != 3 || sr.LastPushError != "HTTP 500" {
			t.Errorf("push failure of C %s = %d, %q; want 3, %q", when, sr.PushFailCount, sr.LastPushError, "HTTP 500")
		}
	}
	if err := storage.MarkResourceDirty(ctx, db, calC, "u1", "event"); err != nil {
		t.Fatalf("MarkResourceDirty C: %v", err)
	}
	for _, c := range []int64{calA, calB, calC} {
		if state(c).Dirty == 1 {
			t.Errorf("calendar %d dirty after the mark of the unowned row; want no mark", c)
		}
	}
	if sr := state(calC); sr.EditUnowned != 1 {
		t.Fatalf("edit_unowned of C = %d, want 1", sr.EditUnowned)
	}
	wantFailure("after the mark")
	unowned, err := q.ListUnownedEditSyncResources(ctx, calC)
	if err != nil || len(unowned) != 1 || unowned[0].Uid != "u1" {
		t.Fatalf("ListUnownedEditSyncResources(C) = %+v, %v; want the row of u1", unowned, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE calendar_id = ?`, calB); err != nil {
		t.Fatalf("delete the master of B: %v", err)
	}
	if err := storage.MarkResourceDirty(ctx, db, calC, "u1", "event"); err != nil {
		t.Fatalf("MarkResourceDirty C again: %v", err)
	}
	if state(calA).Dirty != 1 {
		t.Errorf("A not dirty after the mark with one live master; want the mark")
	}
	if sr := state(calC); sr.EditUnowned != 0 {
		t.Errorf("edit_unowned of C after the owner came back = %d, want 0", sr.EditUnowned)
	}
	wantFailure("after the clear")
}
