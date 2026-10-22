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
// no export holds it. A mark of C marks no calendar dirty. It records the
// edit as a push failure of the sync row of C (NoSeriesOwnerMsg). The edit
// stays local, and the sync status shows it. After the delete of the master of
// B, A owns the override: the mark goes to A, and the failure of C goes.
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
	state := func(calendarID int64) (dirty bool, failures int64, lastErr string) {
		t.Helper()
		sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "u1"})
		if err != nil {
			return false, 0, ""
		}
		return sr.Dirty == 1, sr.PushFailCount, sr.LastPushError
	}
	if err := storage.MarkResourceDirty(ctx, db, calC, "u1", "event"); err != nil {
		t.Fatalf("MarkResourceDirty C: %v", err)
	}
	for _, c := range []int64{calA, calB, calC} {
		if dirty, _, _ := state(c); dirty {
			t.Errorf("calendar %d dirty after the mark of the unowned row; want no mark", c)
		}
	}
	if _, n, msg := state(calC); n != 1 || msg != storage.NoSeriesOwnerMsg {
		t.Fatalf("push failure of C = %d, %q; want 1, %q", n, msg, storage.NoSeriesOwnerMsg)
	}
	failures, err := q.ListSyncResourcePushFailures(ctx, calC)
	if err != nil || len(failures) != 1 || failures[0].Uid != "u1" {
		t.Fatalf("ListSyncResourcePushFailures(C) = %+v, %v; want the row of u1", failures, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE calendar_id = ?`, calB); err != nil {
		t.Fatalf("delete the master of B: %v", err)
	}
	if err := storage.MarkResourceDirty(ctx, db, calC, "u1", "event"); err != nil {
		t.Fatalf("MarkResourceDirty C again: %v", err)
	}
	if dirty, _, _ := state(calA); !dirty {
		t.Errorf("A not dirty after the mark with one live master; want the mark")
	}
	if _, n, msg := state(calC); n != 0 || msg != "" {
		t.Errorf("push failure of C after the owner came back = %d, %q; want none", n, msg)
	}
}
