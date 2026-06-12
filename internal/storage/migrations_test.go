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
