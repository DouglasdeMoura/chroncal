package storage

import (
	"context"
	"testing"
)

func TestOpen_InMemory(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error: %v", err)
	}
	defer db.Close()

	if q == nil {
		t.Fatal("Open returned nil Queries")
	}
}

func TestOpen_MigrationsApplied(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer db.Close()

	tables := []string{"calendars", "events", "event_alarms", "event_attendees", "todos", "todo_alarms", "todo_attendees"}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpen_SeedData(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer db.Close()

	cals, err := q.ListCalendars(context.Background())
	if err != nil {
		t.Fatalf("ListCalendars error: %v", err)
	}
	if len(cals) != 1 {
		t.Fatalf("expected 1 seeded calendar, got %d", len(cals))
	}
	if cals[0].Name != "Personal" {
		t.Errorf("seeded calendar name = %q, want %q", cals[0].Name, "Personal")
	}
}

// TestBackfillAlarmUIDs_TodoOnly guards issue #95: when there are no event
// alarms needing a UID but todo alarms do, the backfill must still assign
// UUIDs to those todo alarms instead of early-returning on the empty event
// alarm list.
func TestBackfillAlarmUIDs_TodoOnly(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Seed a todo (calendar 1 is seeded by Open) and a todo alarm with a
	// NULL uid, simulating a row carried over from the pre-UID schema. No
	// event alarms exist, so the event alarm list is empty.
	res, err := db.ExecContext(ctx,
		`INSERT INTO todos (uid, calendar_id, summary) VALUES (?, 1, ?)`,
		"todo-uid-95", "backfill me")
	if err != nil {
		t.Fatalf("insert todo: %v", err)
	}
	todoID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO todo_alarms (todo_id, action, trigger_value, uid) VALUES (?, 'DISPLAY', '-PT15M', NULL)`,
		todoID); err != nil {
		t.Fatalf("insert todo alarm: %v", err)
	}

	if err := backfillAlarmUIDs(db, q); err != nil {
		t.Fatalf("backfillAlarmUIDs error: %v", err)
	}

	remaining, err := q.ListTodoAlarmsWithEmptyUID(ctx)
	if err != nil {
		t.Fatalf("ListTodoAlarmsWithEmptyUID error: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected 0 todo alarms with empty uid after backfill, got %d", len(remaining))
	}
}

func TestOpen_ForeignKeys(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer db.Close()

	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys error: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}
