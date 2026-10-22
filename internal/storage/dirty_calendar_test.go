package storage_test

import (
	"context"
	"strconv"
	"strings"
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
// edit_unowned on the override row. It makes no sync row: C has none before
// the mark, and none after it. The sync status lists the UID. A real push
// failure of another sync row of C survives the mark and its clear. After the
// delete of the master of B, A owns the override: the mark goes to A, and the
// row mark clears.
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
	// A real push failure of another resource of C.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, push_fail_count, last_push_error)
		 VALUES (?, 'u2', 'event', '/c/u2.ics', 3, 'HTTP 500')`, calC); err != nil {
		t.Fatalf("seed a push failure: %v", err)
	}
	syncRow := func(calendarID int64, uid string) (storage.SyncResource, bool) {
		t.Helper()
		sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
		return sr, err == nil
	}
	rowMark := func() int64 {
		t.Helper()
		var mark int64
		if err := db.QueryRowContext(ctx, `SELECT edit_unowned FROM events WHERE calendar_id = ? AND uid = 'u1'`, calC).Scan(&mark); err != nil {
			t.Fatalf("read the row mark: %v", err)
		}
		return mark
	}
	wantFailure := func(when string) {
		t.Helper()
		if sr, ok := syncRow(calC, "u2"); !ok || sr.PushFailCount != 3 || sr.LastPushError != "HTTP 500" {
			t.Errorf("push failure of u2 %s = %d, %q; want 3, %q", when, sr.PushFailCount, sr.LastPushError, "HTTP 500")
		}
	}
	if err := storage.MarkResourceDirty(ctx, db, calC, "u1", "event"); err != nil {
		t.Fatalf("MarkResourceDirty C: %v", err)
	}
	for _, c := range []int64{calA, calB, calC} {
		if sr, ok := syncRow(c, "u1"); ok && sr.Dirty == 1 {
			t.Errorf("calendar %d dirty after the mark of the unowned row; want no mark", c)
		}
	}
	if _, ok := syncRow(calC, "u1"); ok {
		t.Errorf("the mark made a sync row of u1 in C; want none")
	}
	if got := rowMark(); got != 1 {
		t.Fatalf("edit_unowned of the row = %d, want 1", got)
	}
	wantFailure("after the mark")
	uids, err := q.ListUnownedEditUIDs(ctx, calC)
	if err != nil || len(uids) != 1 || uids[0] != "u1" {
		t.Fatalf("ListUnownedEditUIDs(C) = %v, %v; want u1", uids, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE calendar_id = ?`, calB); err != nil {
		t.Fatalf("delete the master of B: %v", err)
	}
	if err := storage.MarkResourceDirty(ctx, db, calC, "u1", "event"); err != nil {
		t.Fatalf("MarkResourceDirty C again: %v", err)
	}
	if sr, ok := syncRow(calA, "u1"); !ok || sr.Dirty != 1 {
		t.Errorf("A not dirty after the mark with one live master; want the mark")
	}
	if got := rowMark(); got != 0 {
		t.Errorf("edit_unowned of the row after the owner came back = %d, want 0", got)
	}
	wantFailure("after the clear")
}

// TestMarkEventRowDirtyFlagsOnlyTheEditedRow: A and B hold a live master of
// one UID, and C holds two legacy overrides of it. No series owns them. The
// edit of one row marks that row only.
func TestMarkEventRowDirtyFlagsOnlyTheEditedRow(t *testing.T) {
	t.Parallel()
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()
	var cals []int64
	for _, name := range []string{"a", "b", "c"} {
		c, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
		if err != nil {
			t.Fatalf("CreateCalendar %s: %v", name, err)
		}
		cals = append(cals, c.ID)
	}
	ids := map[string]int64{}
	for _, r := range []struct {
		key   string
		cal   int64
		recID string
	}{{"a", cals[0], ""}, {"b", cals[1], ""}, {"c1", cals[2], "2026-05-20T09:00:00Z"}, {"c2", cals[2], "2026-05-21T09:00:00Z"}} {
		res, err := db.ExecContext(ctx,
			`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id)
			 VALUES ('u1', ?, 't', '2026-05-18T09:00:00Z', '2026-05-18T10:00:00Z', ?)`, r.cal, r.recID)
		if err != nil {
			t.Fatalf("insert row: %v", err)
		}
		ids[r.key], _ = res.LastInsertId()
	}
	if err := storage.MarkEventRowDirty(ctx, db, cals[2], "u1", ids["c1"]); err != nil {
		t.Fatalf("MarkEventRowDirty: %v", err)
	}
	for key, want := range map[string]int64{"c1": 1, "c2": 0} {
		var got int64
		if err := db.QueryRowContext(ctx, `SELECT edit_unowned FROM events WHERE id = ?`, ids[key]).Scan(&got); err != nil {
			t.Fatalf("read the mark of %s: %v", key, err)
		}
		if got != want {
			t.Errorf("edit_unowned of %s = %d, want %d", key, got, want)
		}
	}
}

// TestMasterChangeSettlesAnUnownedEdit: A and B hold a live master of one UID,
// and C holds a legacy override with an edit that no series owned. A change
// of a master gives the row one owner again. The trigger of the master then
// marks the calendar of the owner dirty and clears the mark of the row:
//   - a delete of the master of B: A owns the row;
//   - a purge of the master of B: A owns the row;
//   - a move of the master of B to C: C owns the row beside its master;
//   - an insert of a master in C: C owns the row beside its master (issue
//     #862).
func TestMasterChangeSettlesAnUnownedEdit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		stmt  string
		owner string
	}{
		{"delete", `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE id = :b`, "a"},
		{"purge", `DELETE FROM events WHERE id = :b`, "a"},
		{"move", `UPDATE events SET calendar_id = :c WHERE id = :b`, "c"},
		{"insert", `INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id)
			VALUES ('u1', :c, 't', '2026-05-18T09:00:00Z', '2026-05-18T10:00:00Z', '')`, "c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, q := testutil.NewTestDB(t)
			ctx := context.Background()
			account, err := q.CreateAccount(ctx, storage.CreateAccountParams{Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u"})
			if err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			cals := map[string]int64{}
			for _, name := range []string{"a", "b", "c"} {
				c, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
				if err != nil {
					t.Fatalf("CreateCalendar %s: %v", name, err)
				}
				if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
					ID: c.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/" + name + "/"),
				}); err != nil {
					t.Fatalf("LinkCalendarToAccount %s: %v", name, err)
				}
				cals[name] = c.ID
			}
			ids := map[string]int64{}
			for _, r := range []struct {
				key   string
				recID string
			}{{"a", ""}, {"b", ""}, {"c", "2026-05-20T09:00:00Z"}} {
				res, err := db.ExecContext(ctx,
					`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id)
					 VALUES ('u1', ?, 't', '2026-05-18T09:00:00Z', '2026-05-18T10:00:00Z', ?)`, cals[r.key], r.recID)
				if err != nil {
					t.Fatalf("insert row: %v", err)
				}
				ids[r.key], _ = res.LastInsertId()
			}
			if err := storage.MarkEventRowDirty(ctx, db, cals["c"], "u1", ids["c"]); err != nil {
				t.Fatalf("MarkEventRowDirty: %v", err)
			}
			stmt := strings.NewReplacer(":b", strconv.FormatInt(ids["b"], 10), ":c", strconv.FormatInt(cals["c"], 10)).Replace(tc.stmt)
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("change the master of B: %v", err)
			}
			var mark int64
			if err := db.QueryRowContext(ctx, `SELECT edit_unowned FROM events WHERE id = ?`, ids["c"]).Scan(&mark); err != nil || mark != 0 {
				t.Errorf("edit_unowned after the change = %d, %v; want 0", mark, err)
			}
			sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: cals[tc.owner], Uid: "u1"})
			if err != nil || sr.Dirty != 1 {
				t.Errorf("sync row of the owner %s = %+v, %v; want it dirty", tc.owner, sr, err)
			}
		})
	}
}
