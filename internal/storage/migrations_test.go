package storage

import (
	"context"
	"database/sql"
	"testing"
)

// migHelpers returns exec and count helpers bound to conn. The migration
// UpDown tests share them.
func migHelpers(t *testing.T, ctx context.Context, conn *sql.DB) (func(string, ...any), func(string, ...any) int) {
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := conn.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
		return n
	}
	return mustExec, count
}

// migProvider opens the embedded migrations as a goose provider.
func migProvider(t *testing.T, conn *sql.DB) *goose.Provider {
	t.Helper()
	// Build through the production constructor, so a test provider and
	// the one Open uses cannot register different migration sets.
	provider, err := NewMigrationProvider(conn)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	return provider
}

// Verifies migration 031 rolls back and re-applies cleanly (table rebuild +
// trigger recreation in both directions) with live rows. Non-alarm-owned
// X-properties must survive both rebuilds id-intact. Alarm-owned rows are
// intentionally dropped on Down.
func TestMigration031UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, _ := migHelpers(t, ctx, conn)

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

	provider := migProvider(t, conn)
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

// Verifies migration 044 (the wide alarm action CHECK) rolls back and
// re-applies cleanly with live rows. The rebuild runs with foreign keys ON,
// so DROP TABLE performs an implicit DELETE that cascades into alarm_state
// and event_alarm_attendees; the migration must restore those rows
// id-intact. The alarm-owned x_properties rows must survive in place. On
// Down, the migration intentionally deletes an alarm with a sync-only
// action together with its dependent rows.
func TestMigration044UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig044.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, count := migHelpers(t, ctx, conn)

	// Seed: one event with a fireable alarm (state + attendee + x-prop) and
	// one sync-only NONE alarm (x-prop only).
	mustExec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time)
		VALUES ('mig044-evt', 1, 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`)
	mustExec(`INSERT INTO event_alarms (id, event_id, action, trigger_value) VALUES (1, 1, 'EMAIL', '-PT15M')`)
	mustExec(`INSERT INTO event_alarms (id, event_id, action, trigger_value) VALUES (2, 1, 'NONE', '-PT5M')`)
	mustExec(`INSERT INTO alarm_state (id, alarm_id, event_id, trigger_at, fired_at)
		VALUES (7, 1, 1, '2026-01-01T09:45:00Z', '2026-01-01T09:45:01Z')`)
	mustExec(`INSERT INTO event_alarm_attendees (alarm_id, email) VALUES (1, 'a@example.com')`)
	mustExec(`INSERT INTO x_properties (owner_type, owner_id, name, value)
		VALUES ('event_alarm', 1, 'X-KEEP', 'yes')`)
	mustExec(`INSERT INTO x_properties (owner_type, owner_id, name, value)
		VALUES ('event_alarm', 2, 'X-GONE-ON-DOWN', 'yes')`)
	// Seed the todo half with the same shape, so the todo rebuild and the
	// todo child-table backup blocks run against live rows too.
	mustExec(`INSERT INTO todos (uid, calendar_id, summary) VALUES ('mig044-todo', 1, 'Mig')`)
	mustExec(`INSERT INTO todo_alarms (id, todo_id, action, trigger_value) VALUES (1, 1, 'DISPLAY', '-PT15M')`)
	mustExec(`INSERT INTO todo_alarms (id, todo_id, action, trigger_value) VALUES (2, 1, 'NONE', '-PT5M')`)
	mustExec(`INSERT INTO todo_alarm_state (id, alarm_id, todo_id, trigger_at, fired_at)
		VALUES (8, 1, 1, '2026-01-01T09:45:00Z', '2026-01-01T09:45:01Z')`)
	mustExec(`INSERT INTO todo_alarm_attendees (alarm_id, email) VALUES (1, 'b@example.com')`)

	provider := migProvider(t, conn)
	if _, err := provider.DownTo(ctx, 42); err != nil {
		t.Fatalf("down to 42: %v", err)
	}

	if n := count(`SELECT COUNT(*) FROM event_alarms`); n != 1 {
		t.Errorf("alarms after Down = %d, want 1 (the NONE alarm is dropped)", n)
	}
	if n := count(`SELECT COUNT(*) FROM alarm_state WHERE id = 7 AND alarm_id = 1
		AND trigger_at = '2026-01-01T09:45:00Z'`); n != 1 {
		t.Errorf("alarm_state row after Down = %d, want 1 (id-intact restore)", n)
	}
	if n := count(`SELECT COUNT(*) FROM event_alarm_attendees WHERE alarm_id = 1
		AND email = 'a@example.com'`); n != 1 {
		t.Errorf("attendee row after Down = %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM x_properties WHERE owner_type = 'event_alarm'`); n != 1 {
		t.Errorf("alarm x_properties after Down = %d, want 1 (the NONE alarm's rows go with it)", n)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO event_alarms (event_id, action, trigger_value) VALUES (1, 'NONE', '-PT5M')`,
	); err == nil {
		t.Errorf("narrow CHECK accepted action NONE after Down")
	}
	if n := count(`SELECT COUNT(*) FROM todo_alarms`); n != 1 {
		t.Errorf("todo alarms after Down = %d, want 1 (the NONE alarm is dropped)", n)
	}
	if n := count(`SELECT COUNT(*) FROM todo_alarm_state WHERE id = 8 AND alarm_id = 1
		AND trigger_at = '2026-01-01T09:45:00Z'`); n != 1 {
		t.Errorf("todo_alarm_state row after Down = %d, want 1 (id-intact restore)", n)
	}
	if n := count(`SELECT COUNT(*) FROM todo_alarm_attendees WHERE alarm_id = 1
		AND email = 'b@example.com'`); n != 1 {
		t.Errorf("todo attendee row after Down = %d, want 1", n)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	if n := count(`SELECT COUNT(*) FROM todo_alarm_state WHERE id = 8 AND alarm_id = 1`); n != 1 {
		t.Errorf("todo_alarm_state row after re-Up = %d, want 1 (id-intact restore)", n)
	}
	if n := count(`SELECT COUNT(*) FROM todo_alarm_attendees WHERE alarm_id = 1`); n != 1 {
		t.Errorf("todo attendee row after re-Up = %d, want 1", n)
	}

	if n := count(`SELECT COUNT(*) FROM alarm_state WHERE id = 7 AND alarm_id = 1`); n != 1 {
		t.Errorf("alarm_state row after re-Up = %d, want 1 (id-intact restore)", n)
	}
	if n := count(`SELECT COUNT(*) FROM event_alarm_attendees WHERE alarm_id = 1`); n != 1 {
		t.Errorf("attendee row after re-Up = %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM x_properties WHERE owner_type = 'event_alarm'
		AND owner_id = 1 AND name = 'X-KEEP'`); n != 1 {
		t.Errorf("alarm x_properties after re-Up = %d, want 1", n)
	}
	// The wide CHECK accepts a sync-only action again, and the recreated
	// AFTER DELETE trigger still cleans its x_properties.
	mustExec(`INSERT INTO event_alarms (id, event_id, action, trigger_value) VALUES (9, 1, 'X-APPLE-SOUND', '-PT9M')`)
	mustExec(`INSERT INTO x_properties (owner_type, owner_id, name, value)
		VALUES ('event_alarm', 9, 'X-TMP', 'yes')`)
	mustExec(`DELETE FROM event_alarms WHERE id = 9`)
	if n := count(`SELECT COUNT(*) FROM x_properties WHERE owner_type = 'event_alarm' AND owner_id = 9`); n != 0 {
		t.Errorf("x_properties for the deleted alarm = %d, want 0 (cleanup trigger recreated)", n)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO event_alarms (event_id, action, trigger_value) VALUES (1, '', '-PT5M')`,
	); err == nil {
		t.Errorf("wide CHECK accepted an empty action")
	}
}

// Verifies migration 040 (transactional ALTER TABLE ADD COLUMN) round-trips
// cleanly with live data. The four remote_* mirror columns and the partial
// uniqueness index appear on Up and disappear on Down. The calendars.name
// UNIQUE constraint survives both directions. The CHECK constraints enforce
// after Up. Critically, dependent event/todo/journal rows keep their
// foreign keys and ids through the column drop (which internally rebuilds the
// table). This replaces the old NO TRANSACTION table rebuild.
func TestMigration040UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig040.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, _ := migHelpers(t, ctx, conn)

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

	provider := migProvider(t, conn)

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

// The span-column heal is a Go migration, so goose runs it once per
// database and no startup pays for the two table scans. The test pins
// the once-per-database contract: the version is recorded after Open,
// and a legacy row written afterwards stays untouched by a second Up
// (issue #582 round 5).
func TestMigration043RunsOncePerDatabase(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig043.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	var applied int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM goose_db_version WHERE version_id = ?`,
		migration043Version).Scan(&applied); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d applied %d times, want 1", migration043Version, applied)
	}

	// A bad span written after the migration (an older binary could do
	// this) must survive a second Up, which is a no-op.
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, duration)
		 VALUES ('post-migration', 1, 'Later', '2026-01-01T10:00:00Z', '2026-01-01T09:00:00Z', '-PT1H')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	provider, err := NewMigrationProvider(conn)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("second up: %v", err)
	}

	var span string
	if err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(duration, '') FROM events WHERE uid = 'post-migration'`).Scan(&span); err != nil {
		t.Fatalf("read span: %v", err)
	}
	if span != "-PT1H" {
		t.Errorf("duration = %q, want the row untouched by a second Up", span)
	}
}
