package calendar

import (
	"context"
	"database/sql"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// seedLocalItems puts these rows in calendar 1:
//   - a live event master, a live todo, and a live journal
//   - an override of a UID without a master
//   - a deleted event
//   - an event with a tombstone
func seedLocalItems(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time) VALUES ('event', 1, 'Event', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z')`,
		`INSERT INTO todos (uid, calendar_id, summary) VALUES ('todo', 1, 'Todo')`,
		`INSERT INTO journals (uid, calendar_id, summary) VALUES ('journal', 1, 'Journal')`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id) VALUES ('orphan', 1, 'Orphan', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z', '2026-04-03T10:00:00Z')`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, deleted_at) VALUES ('deleted', 1, 'Deleted', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z', '2026-04-04T00:00:00Z')`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time) VALUES ('tombstoned', 1, 'Tombstoned', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z')`,
		`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (1, 'tombstoned', '')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
}

// linkWithCapabilities links calendar 1 to a new account. access and
// components are the capabilities of the remote collection.
func linkWithCapabilities(t *testing.T, svc *Service, q *storage.Queries, db *sql.DB, access, components string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE calendars SET remote_access = ?, remote_components = ? WHERE id = 1`, access, components); err != nil {
		t.Fatalf("set capabilities: %v", err)
	}
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "test", ServerUrl: "https://example.com", AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := svc.LinkToAccount(ctx, 1, account.ID, "https://example.com/cal/work"); err != nil {
		t.Fatalf("LinkToAccount: %v", err)
	}
}

// syncRows returns the owner type of each sync row of calendar 1 by UID.
func syncRows(t *testing.T, q *storage.Queries) map[string]string {
	t.Helper()
	rows, err := q.ListSyncResourcesByCalendar(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListSyncResourcesByCalendar: %v", err)
	}
	got := map[string]string{}
	for _, r := range rows {
		if r.Dirty != 1 || r.RemoteUrl != "" {
			t.Fatalf("sync row %+v; want a dirty row without an href", r)
		}
		got[r.Uid] = r.OwnerType
	}
	return got
}

// TestCalendarService_LinkToAccountMarksTheLocalItems links a local
// calendar. Each live item with a master must get a dirty sync row, so the
// next push uploads it. A deleted item, an item with a tombstone, and an
// override without a master get none (issue #866).
func TestCalendarService_LinkToAccountMarksTheLocalItems(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	seedLocalItems(t, db)
	linkWithCapabilities(t, svc, q, db, "write", "")

	got := syncRows(t, q)
	want := map[string]string{"event": "event", "todo": "todo", "journal": "journal"}
	if len(got) != len(want) {
		t.Fatalf("sync rows = %v, want %v", got, want)
	}
	for uid, owner := range want {
		if got[uid] != owner {
			t.Fatalf("sync rows = %v, want %v", got, want)
		}
	}
}

// TestCalendarService_LinkToAccountSkipsAComponentTheCollectionLacks links
// to a collection that lists VEVENT and VTODO only. The journal must get
// no sync row: the server refuses its create.
func TestCalendarService_LinkToAccountSkipsAComponentTheCollectionLacks(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	seedLocalItems(t, db)
	linkWithCapabilities(t, svc, q, db, "write", "vevent, VTODO")

	got := syncRows(t, q)
	if len(got) != 2 || got["event"] != "event" || got["todo"] != "todo" {
		t.Fatalf("sync rows = %v, want the event and the todo only", got)
	}
}

// TestCalendarService_LinkToAccountUploadsNothingToAReadOnlyCollection
// links to a read-only collection. No item gets a sync row: no push can
// upload it.
func TestCalendarService_LinkToAccountUploadsNothingToAReadOnlyCollection(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	seedLocalItems(t, db)
	linkWithCapabilities(t, svc, q, db, "read", "")

	if got := syncRows(t, q); len(got) != 0 {
		t.Fatalf("sync rows = %v, want none", got)
	}
}
