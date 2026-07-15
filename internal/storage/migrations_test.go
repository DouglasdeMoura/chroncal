package storage

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"

	dbembed "github.com/douglasdemoura/chroncal/db"
)

// Verifies migration 031 rolls back and re-applies cleanly (table rebuild +
// trigger recreation in both directions) with live rows: non-alarm-owned
// X-properties must survive both rebuilds id-intact, alarm-owned rows are
// intentionally dropped on Down.
func TestMigration031UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	// Seed an event, an alarm on it, and X-properties for both owner kinds.
	mustExec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time)
		VALUES ('mig-evt', 1, 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`)
	mustExec(`INSERT INTO event_alarms (event_id, trigger_value) VALUES (1, '-PT15M')`)
	mustExec(`INSERT INTO x_properties (owner_type, owner_id, name, value)
		VALUES ('event', 1, 'X-EVENT-PROP', 'keep')`)
	mustExec(`INSERT INTO x_properties (owner_type, owner_id, name, value)
		VALUES ('event_alarm', 1, 'X-ALARM-PROP', 'dropped-on-down')`)

	var eventPropID int64
	if err := conn.QueryRowContext(ctx,
		`SELECT id FROM x_properties WHERE owner_type = 'event'`).Scan(&eventPropID); err != nil {
		t.Fatalf("query seeded prop: %v", err)
	}

	migrationsFS, err := fs.Sub(dbembed.Migrations, "migrations")
	if err != nil {
		t.Fatalf("sub fs: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationsFS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := provider.DownTo(ctx, 30); err != nil {
		t.Fatalf("down to 30: %v", err)
	}

	var n int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM x_properties WHERE owner_type IN ('event_alarm','todo_alarm')`).Scan(&n); err != nil {
		t.Fatalf("count alarm props: %v", err)
	}
	if n != 0 {
		t.Errorf("alarm-owned x_properties after Down = %d, want 0", n)
	}
	var gotID int64
	var gotValue string
	if err := conn.QueryRowContext(ctx,
		`SELECT id, value FROM x_properties WHERE owner_type = 'event'`).Scan(&gotID, &gotValue); err != nil {
		t.Fatalf("event-owned prop missing after Down: %v", err)
	}
	if gotID != eventPropID || gotValue != "keep" {
		t.Errorf("event prop after Down = (id=%d, value=%q), want (id=%d, value=%q)", gotID, gotValue, eventPropID, "keep")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if err := conn.QueryRowContext(ctx,
		`SELECT id FROM x_properties WHERE owner_type = 'event'`).Scan(&gotID); err != nil {
		t.Fatalf("event-owned prop missing after re-Up: %v", err)
	}
	if gotID != eventPropID {
		t.Errorf("event prop id after re-Up = %d, want %d (rebuild must preserve ids)", gotID, eventPropID)
	}
	// The widened CHECK must accept alarm owners again.
	mustExec(`INSERT INTO x_properties (owner_type, owner_id, name, value)
		VALUES ('event_alarm', 1, 'X-ALARM-PROP', 'works-again')`)
}

// Verifies migration 040 (transactional ALTER TABLE ADD COLUMN) round-trips
// cleanly with live data: the four remote_* mirror columns and the partial
// uniqueness index appear on Up and disappear on Down, the calendars.name
// UNIQUE constraint survives both directions, the CHECK constraints enforce
// after Up, and — critically — dependent event/todo/journal rows keep their
// foreign keys and ids through the column drop (which internally rebuilds the
// table). This replaces the old NO TRANSACTION table rebuild.
func TestMigration040UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig040.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	// Calendar id 1 ('Personal') is seeded by migration 001. Hang dependent
	// rows off it so we can prove foreign keys survive the column rebuild.
	mustExec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time)
		VALUES ('mig040-evt', 1, 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`)
	mustExec(`INSERT INTO todos (uid, calendar_id, summary) VALUES ('mig040-todo', 1, 'Mig Todo')`)
	mustExec(`INSERT INTO journals (uid, calendar_id, summary) VALUES ('mig040-jrnl', 1, 'Mig Journal')`)

	var eventID, todoID, journalID int64
	for _, q := range []struct {
		dst *int64
		sql string
	}{
		{&eventID, `SELECT id FROM events WHERE uid = 'mig040-evt'`},
		{&todoID, `SELECT id FROM todos WHERE uid = 'mig040-todo'`},
		{&journalID, `SELECT id FROM journals WHERE uid = 'mig040-jrnl'`},
	} {
		if err := conn.QueryRowContext(ctx, q.sql).Scan(q.dst); err != nil {
			t.Fatalf("scan dependent id: %v", err)
		}
	}

	hasColumn := func(name string) bool {
		rows, err := conn.QueryContext(ctx, `PRAGMA table_info(calendars)`)
		if err != nil {
			t.Fatalf("pragma: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var col, ctype string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &col, &ctype, &notnull, &dflt, &pk); err != nil {
				t.Fatalf("scan pragma: %v", err)
			}
			if col == name {
				return true
			}
		}
		return false
	}
	hasIndex := func(name string) bool {
		var n int64
		_ = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n)
		return n == 1
	}

	// Post-Up invariants: mirror columns present, partial index present, CHECK
	// constraints enforce, and name is still UNIQUE.
	for _, col := range []string{"remote_name", "remote_access", "remote_components", "remote_missing"} {
		if !hasColumn(col) {
			t.Errorf("after Up, column %q missing from calendars", col)
		}
	}
	if !hasIndex("idx_calendars_account_remote_url") {
		t.Error("after Up, idx_calendars_account_remote_url missing")
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO calendars (name, remote_access) VALUES ('Mig Bad Access', 'admin')`); err == nil {
		t.Error("remote_access CHECK should reject 'admin' after Up")
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO calendars (name) VALUES ('Personal')`); err == nil {
		t.Error("calendars.name UNIQUE constraint not enforced after Up")
	}

	migrationsFS, err := fs.Sub(dbembed.Migrations, "migrations")
	if err != nil {
		t.Fatalf("sub fs: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationsFS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	if _, err := provider.DownTo(ctx, 39); err != nil {
		t.Fatalf("down to 39: %v", err)
	}

	// Post-Down: columns and index gone, but name is still UNIQUE and every
	// dependent row kept its foreign key + id.
	for _, col := range []string{"remote_name", "remote_access", "remote_components", "remote_missing"} {
		if hasColumn(col) {
			t.Errorf("after Down, column %q still present", col)
		}
	}
	if hasIndex("idx_calendars_account_remote_url") {
		t.Error("after Down, idx_calendars_account_remote_url should be dropped")
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO calendars (name) VALUES ('Personal')`); err == nil {
		t.Error("calendars.name UNIQUE constraint not enforced after Down")
	}
	var gotEventID, gotTodoID, gotJournalID, calCount int64
	if err := conn.QueryRowContext(ctx, `SELECT id FROM events WHERE uid = 'mig040-evt'`).Scan(&gotEventID); err != nil {
		t.Fatalf("event missing after Down: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT id FROM todos WHERE uid = 'mig040-todo'`).Scan(&gotTodoID); err != nil {
		t.Fatalf("todo missing after Down: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT id FROM journals WHERE uid = 'mig040-jrnl'`).Scan(&gotJournalID); err != nil {
		t.Fatalf("journal missing after Down: %v", err)
	}
	if gotEventID != eventID || gotTodoID != todoID || gotJournalID != journalID {
		t.Errorf("dependent ids drifted after Down = evt %d->%d, todo %d->%d, jrnl %d->%d",
			eventID, gotEventID, todoID, gotTodoID, journalID, gotJournalID)
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE calendar_id = 1`).Scan(&calCount); err != nil {
		t.Fatalf("count events after Down: %v", err)
	}
	if calCount != 1 {
		t.Errorf("events on calendar 1 after Down = %d, want 1 (FK must survive)", calCount)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	// Post re-Up: columns back and dependent rows still intact with the same
	// ids (the round trip must be lossless).
	if !hasColumn("remote_name") || !hasColumn("remote_missing") {
		t.Error("mirror columns missing after re-Up")
	}
	if err := conn.QueryRowContext(ctx, `SELECT id FROM events WHERE uid = 'mig040-evt'`).Scan(&gotEventID); err != nil {
		t.Fatalf("event missing after re-Up: %v", err)
	}
	if gotEventID != eventID {
		t.Errorf("event id drifted after re-Up = %d, want %d", gotEventID, eventID)
	}
}
