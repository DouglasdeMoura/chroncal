package storage_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// legacyFixture holds a master calendar M and a legacy calendar L. M holds
// the live master of UID legacy-uid. L holds two overrides of that UID, one
// live and one in the trash, and no master.
type legacyFixture struct {
	db   *sql.DB
	q    *storage.Queries
	m, l int64
}

const legacyUID = "legacy-uid"

func newLegacyFixture(t *testing.T) legacyFixture {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	f := legacyFixture{db: db, q: q}
	f.m = f.calendar(t, "M")
	f.l = f.calendar(t, "L")
	f.event(t, f.m, "", false)
	f.event(t, f.l, "2026-04-08T09:00:00Z", false)
	f.event(t, f.l, "2026-04-15T09:00:00Z", true)
	return f
}

func (f legacyFixture) calendar(t *testing.T, name string) int64 {
	t.Helper()
	c, err := f.q.CreateCalendar(context.Background(), storage.CreateCalendarParams{Name: name})
	if err != nil {
		t.Fatalf("CreateCalendar %s: %v", name, err)
	}
	return c.ID
}

// event inserts one row of legacyUID. An empty recurrenceID makes a master.
func (f legacyFixture) event(t *testing.T, calendarID int64, recurrenceID string, trashed bool) int64 {
	t.Helper()
	var deletedAt any
	if trashed {
		deletedAt = "2026-04-01T00:00:00Z"
	}
	rrule := ""
	if recurrenceID == "" {
		rrule = "FREQ=WEEKLY"
	}
	res, err := f.db.Exec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time, all_day, status, transp, sequence, priority, recurrence_id, recurrence_rule, deleted_at)
		VALUES (?, ?, 'Standup', '2026-04-01T09:00:00Z', '2026-04-01T09:30:00Z', 0, 'CONFIRMED', 'OPAQUE', 0, 0, ?, ?, ?)`,
		legacyUID, calendarID, recurrenceID, rrule, deletedAt)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

func (f legacyFixture) link(t *testing.T, calendarID int64, access string) {
	t.Helper()
	ctx := context.Background()
	account, err := f.q.CreateAccount(ctx, storage.CreateAccountParams{Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := f.q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: calendarID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/cal/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := f.db.Exec(`UPDATE calendars SET remote_access = ? WHERE id = ?`, access, calendarID); err != nil {
		t.Fatalf("set remote_access: %v", err)
	}
}

func (f legacyFixture) heal(t *testing.T) {
	t.Helper()
	if err := storage.HealLegacyEventOverrides(context.Background(), f.db, f.q); err != nil {
		t.Fatalf("heal: %v", err)
	}
}

// rowsIn counts the rows of legacyUID in calendarID, live and in the trash.
func (f legacyFixture) rowsIn(t *testing.T, calendarID int64) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM events WHERE uid = ? AND calendar_id = ?`, legacyUID, calendarID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// TestHealLegacyOverrides_MovesLocalRowsHome checks the main case. The two
// overrides of L, the live one and the one in the trash, move into M beside
// their master. The occurrence log of L goes, as for a user move of a legacy
// override back to its master. A second run changes nothing.
func TestHealLegacyOverrides_MovesLocalRowsHome(t *testing.T) {
	t.Parallel()
	f := newLegacyFixture(t)
	if _, err := f.db.Exec(`INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (?, ?, '2026-04-15T09:00:00Z')`, f.l, legacyUID); err != nil {
		t.Fatalf("insert exdate log: %v", err)
	}

	f.heal(t)

	if got := f.rowsIn(t, f.l); got != 0 {
		t.Errorf("rows in L after the heal = %d, want 0", got)
	}
	if got := f.rowsIn(t, f.m); got != 3 {
		t.Errorf("rows in M after the heal = %d, want 3", got)
	}
	var logs int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM event_exdate_deletes WHERE uid = ?`, legacyUID).Scan(&logs); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logs != 0 {
		t.Errorf("occurrence logs after the heal = %d, want 0", logs)
	}

	f.heal(t)
	if got := f.rowsIn(t, f.m); got != 3 {
		t.Errorf("rows in M after a second heal = %d, want 3", got)
	}
}

// TestHealLegacyOverrides_MarksLinkedOwnerDirty checks that a linked owner
// calendar gets the dirty mark, so its server object holds the moved rows.
func TestHealLegacyOverrides_MarksLinkedOwnerDirty(t *testing.T) {
	t.Parallel()
	f := newLegacyFixture(t)
	f.link(t, f.m, "write")

	f.heal(t)

	if got := f.rowsIn(t, f.m); got != 3 {
		t.Fatalf("rows in M after the heal = %d, want 3", got)
	}
	res, err := f.q.GetSyncResource(context.Background(), storage.GetSyncResourceParams{CalendarID: f.m, Uid: legacyUID})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Errorf("dirty = %d, want 1", res.Dirty)
	}
}

// TestHealLegacyOverrides_KeepsRows covers each condition that keeps the
// rows of L where they are.
func TestHealLegacyOverrides_KeepsRows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, f legacyFixture)
	}{
		{"linked source calendar", func(t *testing.T, f legacyFixture) {
			f.link(t, f.l, "write")
		}},
		{"sync row of the uid in the source", func(t *testing.T, f legacyFixture) {
			if _, err := f.db.Exec(`INSERT INTO sync_resources (calendar_id, uid, owner_type) VALUES (?, ?, 'event')`, f.l, legacyUID); err != nil {
				t.Fatalf("insert sync row: %v", err)
			}
		}},
		{"tombstone of the uid in the source", func(t *testing.T, f legacyFixture) {
			if _, err := f.db.Exec(`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (?, ?, '/cal/x.ics')`, f.l, legacyUID); err != nil {
				t.Fatalf("insert tombstone: %v", err)
			}
		}},
		{"owner master in the trash", func(t *testing.T, f legacyFixture) {
			if _, err := f.db.Exec(`UPDATE events SET deleted_at = '2026-04-02T00:00:00Z' WHERE calendar_id = ? AND recurrence_id = ''`, f.m); err != nil {
				t.Fatalf("trash master: %v", err)
			}
		}},
		{"read-only owner calendar", func(t *testing.T, f legacyFixture) {
			f.link(t, f.m, "read")
		}},
		{"collision in the owner calendar", func(t *testing.T, f legacyFixture) {
			f.event(t, f.m, "2026-04-08T09:00:00Z", false)
		}},
		{"collision with a row in the trash", func(t *testing.T, f legacyFixture) {
			f.event(t, f.m, "2026-04-15T09:00:00Z", true)
		}},
		{"two live masters", func(t *testing.T, f legacyFixture) {
			f.event(t, f.calendar(t, "N"), "", false)
		}},
		{"open conflict in the owner calendar", func(t *testing.T, f legacyFixture) {
			if _, err := f.db.Exec(`INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag) VALUES (?, 'event', 1, ?, '', '', '')`, f.m, legacyUID); err != nil {
				t.Fatalf("insert conflict: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newLegacyFixture(t)
			tc.setup(t, f)

			f.heal(t)

			if got := f.rowsIn(t, f.l); got != 2 {
				t.Errorf("rows in L after the heal = %d, want 2", got)
			}
		})
	}
}

// TestOpenHealsLegacyOverrides checks the startup wiring: Open runs the
// heal on a database that an older version left.
func TestOpenHealsLegacyOverrides(t *testing.T) {
	t.Parallel()
	path := testutil.DBPath(t)
	db, q, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f := legacyFixture{db: db, q: q}
	f.m = f.calendar(t, "M")
	f.l = f.calendar(t, "L")
	f.event(t, f.m, "", false)
	f.event(t, f.l, "2026-04-08T09:00:00Z", false)
	db.Close()

	db, _, err = storage.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE uid = ? AND calendar_id = ?`, legacyUID, f.l).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rows in L after reopen = %d, want 0", n)
	}
}
