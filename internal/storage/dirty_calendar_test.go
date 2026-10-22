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
