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
