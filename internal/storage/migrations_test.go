package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	dbembed "github.com/douglasdemoura/chroncal/db"
	"github.com/douglasdemoura/chroncal/internal/model"
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

// Verifies migration 045 repairs a malformed stored alarm action and
// leaves every well-shaped value alone. A build older than the token rule
// of issue #595 stored any non-empty action, so a row can hold " " or
// "NO NE". A preserved foreign action such as X-APPLE-SOUND holds only
// token characters and must survive untouched.
func TestMigration045RepairsMalformedAction(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig045.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, count := migHelpers(t, ctx, conn)
	provider := migProvider(t, conn)

	// Go back before the repair, so the seeds can hold the malformed
	// values an older build stored.
	if _, err := provider.DownTo(ctx, 44); err != nil {
		t.Fatalf("down to 44: %v", err)
	}

	mustExec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time)
		VALUES ('mig045-evt', 1, 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`)
	mustExec(`INSERT INTO todos (uid, calendar_id, summary) VALUES ('mig045-todo', 1, 'Mig')`)

	// The lowercase seed matters: parseAlarm keeps the original case of a
	// preserved action, so a pattern that covered only uppercase would
	// clobber it and both tests would still pass.
	seeds := []struct {
		id     int
		action string
		repair bool
	}{
		{1, " ", true},
		{2, "NO NE", true},
		{3, "X-APPLE-SOUND", false},
		{4, "NONE", false},
		{5, "DISPLAY", false},
		{6, "PROCEDURE", false},
		{7, "x-apple-sound", false},
		{8, "a.b", true},
	}
	for _, s := range seeds {
		mustExec(`INSERT INTO event_alarms (id, event_id, action, trigger_value) VALUES (?, 1, ?, '-PT15M')`,
			s.id, s.action)
		mustExec(`INSERT INTO todo_alarms (id, todo_id, action, trigger_value) VALUES (?, 1, ?, '-PT15M')`,
			s.id, s.action)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	for _, table := range []string{"event_alarms", "todo_alarms"} {
		for _, s := range seeds {
			want := s.action
			if s.repair {
				want = model.UnsupportedAlarmAction
			}
			if n := count(`SELECT COUNT(*) FROM `+table+` WHERE id = ? AND action = ?`, s.id, want); n != 1 {
				t.Errorf("%s id %d: action %q did not end as %q", table, s.id, s.action, want)
			}
		}
	}
}

// migration045Pattern returns the GLOB literal the migration 045 file
// holds, including its quotes. The pattern test probes that value, so an
// edit to the migration reaches the test.
func migration045Pattern(t *testing.T) string {
	t.Helper()
	body, err := dbembed.Migrations.ReadFile("migrations/045_repair_malformed_alarm_action.sql")
	if err != nil {
		t.Fatalf("read migration 045: %v", err)
	}
	const marker = "GLOB "
	var found []string
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		i := strings.Index(trimmed, marker)
		if i < 0 {
			continue
		}
		rest := trimmed[i+len(marker):]
		end := strings.Index(rest[1:], "'")
		if !strings.HasPrefix(rest, "'") || end < 0 {
			t.Fatalf("migration 045: cannot read the GLOB literal from %q", trimmed)
		}
		found = append(found, rest[:end+2])
	}
	// The migration repairs both alarm tables. Read every pattern, so a
	// narrowed literal in either statement fails this test.
	if len(found) != 2 {
		t.Fatalf("migration 045: found %d GLOB patterns, want one per alarm table", len(found))
	}
	if found[0] != found[1] {
		t.Fatalf("migration 045: the two statements use different patterns: %s and %s", found[0], found[1])
	}
	return found[0]
}

// The migration 045 GLOB pattern and model.ValidAlarmActionToken must give
// the same verdict. A value the Go rule refuses must be repaired, and a
// value it accepts must survive. Two hand-written rules would otherwise
// drift, and the migration would leave a row every later write refuses.
func TestMigration045PatternMatchesTokenRule(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig045-pattern.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	// Read the predicate out of the migration itself. A copy typed here
	// would keep passing after an edit to the migration, which is the
	// drift this test exists to catch.
	pattern := migration045Pattern(t)

	// The empty value is out of scope. The CHECK constraint of migration
	// 044 forbids it, and every other path reads it as "unset" and uses
	// DISPLAY, so the migration must not rewrite it to the reserved token.
	candidates := []string{
		"DISPLAY", "AUDIO", "EMAIL", "NONE", "PROCEDURE", "X-APPLE-SOUND",
		"x-lower-case", "A1", "-", "9",
		" ", "  ", "\t", "NO NE", "X APPLE", "DISPLAY;", "a.b", "caf\u00e9",
	}
	for _, v := range candidates {
		var matched int
		if err := conn.QueryRowContext(context.Background(),
			`SELECT CASE WHEN ? GLOB `+pattern+` THEN 1 ELSE 0 END`,
			v).Scan(&matched); err != nil {
			t.Fatalf("probe %q: %v", v, err)
		}
		wantRepair := !model.ValidAlarmActionToken(v)
		if (matched == 1) != wantRepair {
			t.Errorf("action %q: the migration repairs it = %v, but the token rule calls it malformed = %v",
				v, matched == 1, wantRepair)
		}
	}
}

// Verifies migration 051 (the wide RELTYPE CHECK) rolls back and re-applies
// cleanly with live rows in the three relation tables. RELTYPE is an
// extensible token in RFC 5545, so the widened CHECK must accept a
// server-specific token such as X-APPLE-SOMETHING (issue #768). The Down step
// restores the narrow CHECK and drops each row that carries another token.
func TestMigration051UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig051.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, count := migHelpers(t, ctx, conn)

	mustExec(`INSERT INTO events (id, uid, calendar_id, title, start_time, end_time)
		VALUES (1, 'mig051-evt', 1, 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`)
	mustExec(`INSERT INTO todos (id, uid, calendar_id, summary) VALUES (1, 'mig051-todo', 1, 'Mig')`)
	mustExec(`INSERT INTO journals (id, uid, calendar_id, summary, start_date)
		VALUES (1, 'mig051-jnl', 1, 'Mig', '2026-01-01')`)

	// One standard token and one server-specific token per table. The
	// widened CHECK must accept both.
	mustExec(`INSERT INTO event_relations (id, event_id, rel_type, rel_uid)
		VALUES (5, 1, 'CHILD', 'evt-child')`)
	mustExec(`INSERT INTO event_relations (id, event_id, rel_type, rel_uid)
		VALUES (6, 1, 'X-APPLE-SOMETHING', 'evt-apple')`)
	mustExec(`INSERT INTO todo_relations (id, todo_id, rel_type, rel_uid)
		VALUES (5, 1, 'SIBLING', 'todo-sibling')`)
	mustExec(`INSERT INTO todo_relations (id, todo_id, rel_type, rel_uid)
		VALUES (6, 1, 'X-APPLE-SOMETHING', 'todo-apple')`)
	mustExec(`INSERT INTO journal_relations (id, journal_id, rel_type, rel_uid)
		VALUES (5, 1, 'PARENT', 'jnl-parent')`)
	mustExec(`INSERT INTO journal_relations (id, journal_id, rel_type, rel_uid)
		VALUES (6, 1, 'X-APPLE-SOMETHING', 'jnl-apple')`)

	// The widened CHECK still refuses an empty token.
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO event_relations (event_id, rel_type, rel_uid) VALUES (1, '', 'evt-empty')`,
	); err == nil {
		t.Errorf("wide CHECK accepted an empty rel_type")
	}

	provider := migProvider(t, conn)
	if _, err := provider.DownTo(ctx, 50); err != nil {
		t.Fatalf("down to 50: %v", err)
	}

	for _, table := range []string{"event_relations", "todo_relations", "journal_relations"} {
		if n := count(`SELECT COUNT(*) FROM ` + table); n != 1 {
			t.Errorf("%s rows after Down = %d, want 1 (the unknown token is dropped)", table, n)
		}
		if n := count(`SELECT COUNT(*) FROM ` + table + ` WHERE id = 5`); n != 1 {
			t.Errorf("%s row id 5 after Down = %d, want 1 (id-intact restore)", table, n)
		}
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO event_relations (event_id, rel_type, rel_uid) VALUES (1, 'X-APPLE-SOMETHING', 'evt-again')`,
	); err == nil {
		t.Errorf("narrow CHECK accepted an unknown rel_type after Down")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	for _, table := range []string{"event_relations", "todo_relations", "journal_relations"} {
		if n := count(`SELECT COUNT(*) FROM ` + table + ` WHERE id = 5`); n != 1 {
			t.Errorf("%s row id 5 after re-Up = %d, want 1 (id-intact restore)", table, n)
		}
	}
	// The widened CHECK accepts a server-specific token again, and the
	// recreated foreign key still cascades a delete of the owner row.
	mustExec(`INSERT INTO event_relations (id, event_id, rel_type, rel_uid)
		VALUES (9, 1, 'X-CALENDARSERVER-LINK', 'evt-link')`)
	mustExec(`DELETE FROM events WHERE id = 1`)
	if n := count(`SELECT COUNT(*) FROM event_relations`); n != 0 {
		t.Errorf("event_relations after the owner delete = %d, want 0 (cascade recreated)", n)
	}
}

// TestMigration059KeepsOldTombstonesAsPlainDeletes upgrades a database
// with a tombstone in a source calendar beside a sync row without a server
// object in a sibling calendar. That state has no record of a move, so the
// migration must leave the tombstone as a plain delete.
func TestMigration059KeepsOldTombstonesAsPlainDeletes(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig059.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, count := migHelpers(t, ctx, conn)
	provider := migProvider(t, conn)
	if _, err := provider.DownTo(ctx, 58); err != nil {
		t.Fatalf("down to 58: %v", err)
	}

	mustExec(`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (1, 'acct', 'https://example.com', 'basic', 'u')`)
	mustExec(`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (10, 'A', 1, '/cal/a/')`)
	mustExec(`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (11, 'B', 1, '/cal/b/')`)
	mustExec(`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		VALUES (10, 'moved', 'event', '/cal/a/moved.ics', 'e1', 0, 'sync-token')`)
	mustExec(`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		VALUES (11, 'moved', 'event', '', '', 1, 'sync-token')`)
	mustExec(`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (10, 'moved', '/cal/a/moved.ics')`)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if n := count(`SELECT COUNT(*) FROM tombstones WHERE uid = 'moved' AND moved_to_calendar_id IS NULL`); n != 1 {
		t.Errorf("old tombstone without a target = %d, want 1", n)
	}
}

// TestMigration060WaitsForALegacyMoveTarget upgrades a database with a move
// from before the upgrade. The source has a tombstone without a move target.
// A sibling calendar of the same account has a sync row without a server
// object. The view must hold that tombstone, so its source delete waits for
// the create. A plain tombstone without such a sibling row must not wait.
func TestMigration060WaitsForALegacyMoveTarget(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig060.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, count := migHelpers(t, ctx, conn)
	provider := migProvider(t, conn)
	if _, err := provider.DownTo(ctx, 58); err != nil {
		t.Fatalf("down to 58: %v", err)
	}
	mustExec(`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (1, 'acct', 'https://example.com', 'basic', 'u')`)
	mustExec(`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (10, 'A', 1, '/cal/a/')`)
	mustExec(`INSERT INTO calendars (id, name, account_id, remote_url) VALUES (11, 'B', 1, '/cal/b/')`)
	mustExec(`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		VALUES (11, 'moved', 'event', '', '', 1, 'sync-token')`)
	mustExec(`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (10, 'moved', '/cal/a/moved.ics')`)
	mustExec(`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (10, 'plain', '/cal/a/plain.ics')`)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if n := count(`SELECT COUNT(*) FROM move_wait_tombstones WHERE uid = 'moved'`); n != 1 {
		t.Errorf("legacy move tombstone in the wait view = %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM move_wait_tombstones WHERE uid = 'plain'`); n != 0 {
		t.Errorf("plain tombstone in the wait view = %d, want 0", n)
	}
	if n := count(`SELECT COUNT(*) FROM tombstones WHERE uid = 'moved' AND legacy_move = 1`); n != 1 {
		t.Errorf("legacy tombstone after the upgrade = %d, want legacy_move 1", n)
	}
	// A new plain delete beside the same unpushed sibling row never waits.
	mustExec(`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		VALUES (11, 'fresh', 'event', '', '', 1, 'sync-token')`)
	mustExec(`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (10, 'fresh', '/cal/a/fresh.ics')`)
	if n := count(`SELECT COUNT(*) FROM move_wait_tombstones WHERE uid = 'fresh'`); n != 0 {
		t.Errorf("new plain tombstone in the wait view = %d, want 0", n)
	}
}

// TestMigration057UpDown covers the per-calendar keys of the delete logs
// (issue #842). After Up, two calendars keep their own row of one
// occurrence and of one cutoff. Down keeps the first row of each key.
func TestMigration057UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig057.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, count := migHelpers(t, ctx, conn)

	mustExec(`INSERT INTO calendars (id, name) VALUES (2, 'Other')`)
	mustExec(`INSERT INTO event_exdate_deletes (id, calendar_id, uid, recurrence_id)
		VALUES (5, 1, 'mig057', '2026-01-02T10:00:00Z')`)
	mustExec(`INSERT INTO event_exdate_deletes (id, calendar_id, uid, recurrence_id)
		VALUES (6, 2, 'mig057', '2026-01-02T10:00:00Z')`)
	mustExec(`INSERT INTO event_truncate_deletes (id, calendar_id, uid, cutoff_time, previous_rrule, hidden_overrides, removed_rdates)
		VALUES (5, 1, 'mig057', '2026-01-03T10:00:00Z', 'FREQ=DAILY', '', '')`)
	mustExec(`INSERT INTO event_truncate_deletes (id, calendar_id, uid, cutoff_time, previous_rrule, hidden_overrides, removed_rdates)
		VALUES (6, 2, 'mig057', '2026-01-03T10:00:00Z', 'FREQ=DAILY', '', '')`)

	// The same occurrence in the same calendar still conflicts.
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (1, 'mig057', '2026-01-02T10:00:00Z')`,
	); err == nil {
		t.Errorf("the per-calendar key accepted a second row of one occurrence in one calendar")
	}
	// The series-delete log keeps one row per series and calendar, and
	// the view lists the rows of a series.
	// One log row per series delete: two deletes in one calendar keep two
	// rows.
	mustExec(`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids) VALUES (1, 'mig057', '')`)
	mustExec(`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids) VALUES (1, 'mig057', 'x')`)
	if n := count(`SELECT COUNT(*) FROM event_series_deletes WHERE calendar_id = 1 AND uid = 'mig057'`); n != 2 {
		t.Errorf("series-delete log rows of one series = %d, want 2", n)
	}
	// The view check takes its own UID: the insert of a master drops the
	// delete logs of its UID in its calendar (events_master_insert_drops_stale_logs).
	mustExec(`INSERT INTO events (id, uid, calendar_id, title, start_time, end_time)
		VALUES (1, 'mig057-view', 1, 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`)
	if n := count(`SELECT COUNT(*) FROM event_series_rows WHERE uid = 'mig057-view' AND owner_calendar_id = 1`); n != 1 {
		t.Errorf("event_series_rows of the master = %d, want 1", n)
	}

	provider := migProvider(t, conn)
	if _, err := provider.DownTo(ctx, 56); err != nil {
		t.Fatalf("down to 56: %v", err)
	}
	for _, object := range []string{"event_series_deletes", "event_series_rows"} {
		if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE name = '` + object + `'`); n != 0 {
			t.Errorf("%s exists after Down", object)
		}
	}
	for _, table := range []string{"event_exdate_deletes", "event_truncate_deletes"} {
		if n := count(`SELECT COUNT(*) FROM ` + table); n != 1 {
			t.Errorf("%s rows after Down = %d, want 1 (the second calendar's row is dropped)", table, n)
		}
		if n := count(`SELECT COUNT(*) FROM ` + table + ` WHERE id = 5 AND calendar_id = 1`); n != 1 {
			t.Errorf("%s row id 5 after Down = %d, want 1 (id-intact restore)", table, n)
		}
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	mustExec(`INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id)
		VALUES (2, 'mig057', '2026-01-02T10:00:00Z')`)
	mustExec(`INSERT INTO event_truncate_deletes (calendar_id, uid, cutoff_time, previous_rrule)
		VALUES (2, 'mig057', '2026-01-03T10:00:00Z', 'FREQ=DAILY')`)
	for _, table := range []string{"event_exdate_deletes", "event_truncate_deletes"} {
		if n := count(`SELECT COUNT(*) FROM ` + table); n != 2 {
			t.Errorf("%s rows after re-Up and a second calendar's insert = %d, want 2", table, n)
		}
	}
	// The recreated foreign key still cascades a delete of the calendar.
	mustExec(`DELETE FROM calendars WHERE id = 2`)
	for _, table := range []string{"event_exdate_deletes", "event_truncate_deletes"} {
		if n := count(`SELECT COUNT(*) FROM ` + table + ` WHERE calendar_id = 2`); n != 0 {
			t.Errorf("%s rows of the deleted calendar = %d, want 0 (cascade recreated)", table, n)
		}
	}
}

// seriesViewStatement reads the CREATE VIEW statement of event_series_rows
// from migration 057. The rebuild test then runs the statement that the
// migrations README tells a rebuild to copy.
func seriesViewStatement(t *testing.T) string {
	t.Helper()
	src, err := dbembed.Migrations.ReadFile("migrations/057_delete_logs_uid_unique_per_calendar.sql")
	if err != nil {
		t.Fatalf("read migration 057: %v", err)
	}
	start := strings.Index(string(src), "CREATE VIEW event_series_rows AS")
	if start < 0 {
		t.Fatal("migration 057 has no CREATE VIEW event_series_rows")
	}
	end := strings.Index(string(src[start:]), ";")
	if end < 0 {
		t.Fatal("the CREATE VIEW statement has no end")
	}
	return string(src[start : start+end+1])
}

// moveCopiesViewStatement reads the CREATE VIEW statement of
// live_move_copies from migration 060, for step 4 of the rebuild in the
// migrations README.
func moveCopiesViewStatement(t *testing.T) string {
	t.Helper()
	src, err := dbembed.Migrations.ReadFile("migrations/060_move_wait_tombstones.sql")
	if err != nil {
		t.Fatalf("read migration 060: %v", err)
	}
	start := strings.Index(string(src), "CREATE VIEW live_move_copies AS")
	if start < 0 {
		t.Fatal("migration 060 has no CREATE VIEW live_move_copies")
	}
	end := strings.Index(string(src[start:]), ";")
	if end < 0 {
		t.Fatal("the CREATE VIEW statement has no end")
	}
	return string(src[start : start+end+1])
}

// rebuildEvents runs the table-rebuild pattern of migration 050 on a
// test database: drop the triggers on events, copy events into
// events_new, drop events, rename.
func rebuildEvents(ctx context.Context, conn *sql.DB) error {
	for _, stmt := range []string{
		`PRAGMA foreign_keys = OFF`,
		`DROP TRIGGER IF EXISTS events_fts_ai`,
		`DROP TRIGGER IF EXISTS events_fts_au`,
		`DROP TRIGGER IF EXISTS events_fts_bd`,
		`DROP TRIGGER IF EXISTS events_x_properties_ad`,
		`DROP TRIGGER IF EXISTS event_alarms_x_properties_ad`,
		`DROP TRIGGER IF EXISTS x_properties_event_owner_exists`,
		`DROP TRIGGER IF EXISTS event_categories_fts_ai`,
		`DROP TRIGGER IF EXISTS event_categories_fts_ad`,
		`CREATE TABLE events_new AS SELECT * FROM events`,
		`DROP TABLE events`,
		`ALTER TABLE events_new RENAME TO events`,
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// TestEventsRebuildKeepsSeriesView proves the sequence that the migrations
// README gives for a rebuild of the events table. SQLite refuses the rename
// while the view event_series_rows reads events. The rebuild works when it
// drops the view first and creates it again after.
func TestEventsRebuildKeepsSeriesView(t *testing.T) {
	ctx := context.Background()
	t.Run("rename fails with the view in place", func(t *testing.T) {
		conn := openFixtureDB(t)
		if err := rebuildEvents(ctx, conn); err == nil {
			t.Fatal("the rebuild succeeded with the view in place, want the rename to fail")
		}
	})
	t.Run("drop, rebuild, create again", func(t *testing.T) {
		conn := openFixtureDB(t)
		mustExec, count := migHelpers(t, ctx, conn)
		mustExec(`INSERT INTO events (id, uid, calendar_id, title, start_time, end_time)
			VALUES (1, 'rebuild', 1, 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`)
		mustExec(`DROP VIEW IF EXISTS event_series_rows`)
		mustExec(`DROP VIEW IF EXISTS live_move_copies`)
		if err := rebuildEvents(ctx, conn); err != nil {
			t.Fatalf("rebuild with the view dropped: %v", err)
		}
		mustExec(seriesViewStatement(t))
		// README step 4: the view of the moved copies of migration 060.
		mustExec(moveCopiesViewStatement(t))
		if n := count(`SELECT COUNT(*) FROM live_move_copies WHERE uid = 'rebuild'`); n != 1 {
			t.Errorf("live_move_copies after the rebuild = %d, want 1", n)
		}
		if n := count(`SELECT COUNT(*) FROM event_series_rows WHERE uid = 'rebuild' AND owner_calendar_id = 1`); n != 1 {
			t.Errorf("event_series_rows after the rebuild = %d, want 1", n)
		}
		// README step 5: the DROP TABLE of the rebuild drops the triggers.
		triggers := []string{
			"events_master_revive_drops_series_log", "events_master_insert_drops_stale_logs",
			"events_override_revive_drops_stale_logs", "events_override_insert_drops_prune_marker",
			"events_purge_drops_row_from_logs",
			"events_master_change_settles_unowned_edits", "events_master_purge_settles_unowned_edits",
		}
		for _, name := range triggers {
			q := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = '` + name + `'`
			if n := count(q); n != 0 {
				t.Fatalf("trigger %s after the rebuild = %d, want 0 before step 5", name, n)
			}
			mustExec(triggerStatement(t, name))
			if n := count(q); n != 1 {
				t.Fatalf("trigger %s after step 5 = %d, want 1", name, n)
			}
		}
		// README step 6: the settle trigger of a master insert, migration 061.
		const insertSettle = "events_master_insert_settles_unowned_edits"
		mustExec(triggerStatementOf(t, "061_master_insert_settles_unowned_edits.sql", insertSettle))
		if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = '` + insertSettle + `'`); n != 1 {
			t.Fatalf("trigger %s after step 6 = %d, want 1", insertSettle, n)
		}
		const logs = `SELECT COUNT(*) FROM event_series_deletes WHERE calendar_id = 1 AND uid = 'rebuild'`
		mustExec(`UPDATE events SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = 1`)
		// The log of the master: a master from before the token consumes a
		// log with a master only.
		mustExec(`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master) VALUES (1, 'rebuild', '', 1)`)
		mustExec(`UPDATE events SET deleted_at = NULL WHERE id = 1`)
		if n := count(logs); n != 0 {
			t.Errorf("series log after the revive = %d, want 0 (the revive trigger drops it)", n)
		}
		mustExec(`DELETE FROM events WHERE id = 1`)
		mustExec(`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids) VALUES (1, 'rebuild', '')`)
		// The copy of the test rebuild has no column defaults, so the insert
		// names recurrence_id.
		mustExec(`INSERT INTO events (id, uid, calendar_id, recurrence_id, title, start_time, end_time)
			VALUES (2, 'rebuild', 1, '', 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`)
		if n := count(logs); n != 0 {
			t.Errorf("series log after the insert = %d, want 0 (the insert trigger drops it)", n)
		}
	})
}

// triggerStatement reads the CREATE TRIGGER statement of name from
// migration 057, so the rebuild test runs step 5 of the migrations README.
func triggerStatement(t *testing.T, name string) string {
	t.Helper()
	return triggerStatementOf(t, "057_delete_logs_uid_unique_per_calendar.sql", name)
}

// triggerStatementOf reads the CREATE TRIGGER statement of name from the
// migration file.
func triggerStatementOf(t *testing.T, file, name string) string {
	t.Helper()
	src, err := dbembed.Migrations.ReadFile("migrations/" + file)
	if err != nil {
		t.Fatalf("read migration %s: %v", file, err)
	}
	start := strings.Index(string(src), "CREATE TRIGGER "+name)
	if start < 0 {
		t.Fatalf("migration %s has no CREATE TRIGGER %s", file, name)
	}
	end := strings.Index(string(src[start:]), "END;")
	if end < 0 {
		t.Fatal("the CREATE TRIGGER statement has no end")
	}
	return string(src[start : start+end+len("END;")])
}

// TestMigrationsReadmeMatches057 compares each SQL block of the migrations
// README with the statement of migration 057 that it copies: the view and
// the four triggers. A change of migration 057 then cannot leave the README
// copy behind.
func TestMigrationsReadmeMatches057(t *testing.T) {
	readme, err := os.ReadFile("../../db/migrations/README.md")
	if err != nil {
		t.Fatalf("read the README: %v", err)
	}
	var blocks []string
	for _, part := range strings.Split(string(readme), "```sql\n")[1:] {
		end := strings.Index(part, "```")
		if end < 0 {
			t.Fatal("a SQL block of the README has no end")
		}
		blocks = append(blocks, strings.TrimSpace(part[:end]))
	}
	want := []string{
		seriesViewStatement(t),
		triggerStatement(t, "events_master_revive_drops_series_log"),
		triggerStatement(t, "events_master_insert_drops_stale_logs"),
		triggerStatement(t, "events_override_revive_drops_stale_logs"),
		triggerStatement(t, "events_override_insert_drops_prune_marker"),
		triggerStatement(t, "events_purge_drops_row_from_logs"),
		triggerStatement(t, "events_master_change_settles_unowned_edits"),
		triggerStatement(t, "events_master_purge_settles_unowned_edits"),
	}
	if len(blocks) != len(want) {
		t.Fatalf("README SQL blocks = %d, want %d", len(blocks), len(want))
	}
	for i := range want {
		if blocks[i] != strings.TrimSpace(want[i]) {
			t.Errorf("README SQL block %d differs from migration 057:\nREADME:\n%s\nmigration:\n%s", i, blocks[i], want[i])
		}
	}
}

// TestMigration057_UIDIndexesOfTheLogs: the triggers and the identity check
// of an undo read the three delete logs by UID, so each log has an index on
// uid.
func TestMigration057_UIDIndexesOfTheLogs(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/uid-index.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	_, count := migHelpers(t, context.Background(), conn)
	for _, name := range []string{"idx_event_exdate_deletes_uid", "idx_event_truncate_deletes_uid", "idx_event_series_deletes_uid"} {
		if n := count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = '` + name + `'`); n != 1 {
			t.Errorf("index %s = %d, want 1", name, n)
		}
	}
}

// Verifies migration 063: an alarm UID is unique in its event or its todo
// only (issue #922). Down keeps the UID on the row with the lowest ID.
func TestMigration063UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	mustExec, count := migHelpers(t, ctx, conn)
	for _, uid := range []string{"e1", "e2"} {
		mustExec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time)
			VALUES (?, 1, 'Mig', '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z')`, uid)
		mustExec(`INSERT INTO todos (uid, calendar_id, summary) VALUES (?, 1, 'Mig')`, uid)
	}
	for _, id := range []int{1, 2} {
		mustExec(`INSERT INTO event_alarms (event_id, uid, trigger_value) VALUES (?, 'shared', '-PT15M')`, id)
		mustExec(`INSERT INTO todo_alarms (todo_id, uid, trigger_value) VALUES (?, 'shared', '-PT15M')`, id)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO event_alarms (event_id, uid, trigger_value) VALUES (1, 'shared', '-PT5M')`); err == nil {
		t.Error("a second use of a UID in one event must fail")
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO todo_alarms (todo_id, uid, trigger_value) VALUES (1, 'shared', '-PT5M')`); err == nil {
		t.Error("a second use of a UID in one todo must fail")
	}

	provider := migProvider(t, conn)
	if _, err := provider.DownTo(ctx, 62); err != nil {
		t.Fatalf("down to 62: %v", err)
	}
	for _, table := range []string{"event_alarms", "todo_alarms"} {
		if n := count(`SELECT COUNT(*) FROM ` + table + ` WHERE uid = 'shared'`); n != 1 {
			t.Errorf("%s rows with the shared UID after Down = %d, want 1", table, n)
		}
		if n := count(`SELECT COUNT(*) FROM ` + table + ` WHERE uid = 'shared' AND id = (SELECT MIN(id) FROM ` + table + `)`); n != 1 {
			t.Errorf("%s: the row with the lowest ID must keep the UID", table)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	mustExec(`UPDATE event_alarms SET uid = 'shared'`)
	mustExec(`UPDATE todo_alarms SET uid = 'shared'`)
}
