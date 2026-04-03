package storage

import (
	"context"
	"testing"
)

// TestScanColumnAlignment opens a DB with all migrations applied, inserts
// minimal rows into events, todos, and journals, then calls the dynamic query
// functions to verify scan succeeds without column-count panics. This catches
// the case where a migration adds a column but scan_helpers.go is not updated.
func TestScanColumnAlignment(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Use the seeded "Personal" calendar
	cals, err := q.ListCalendars(ctx)
	if err != nil || len(cals) == 0 {
		t.Fatalf("list calendars: %v (count=%d)", err, len(cals))
	}
	calID := cals[0].ID

	// Insert a minimal event
	_, err = db.Exec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time, all_day, status, transp, sequence, priority) VALUES ('scan-test-event', ?, 'Test', '2026-04-01T00:00:00Z', '2026-04-01T01:00:00Z', 0, 'CONFIRMED', 'OPAQUE', 0, 0)`, calID)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	// Insert a minimal todo
	_, err = db.Exec(`INSERT INTO todos (uid, calendar_id, summary, status, priority, sequence) VALUES ('scan-test-todo', ?, 'Test', 'NEEDS-ACTION', 0, 0)`, calID)
	if err != nil {
		t.Fatalf("insert todo: %v", err)
	}

	// Insert a minimal journal
	_, err = db.Exec(`INSERT INTO journals (uid, calendar_id, summary, status, sequence) VALUES ('scan-test-journal', ?, 'Test', 'FINAL', 0)`, calID)
	if err != nil {
		t.Fatalf("insert journal: %v", err)
	}

	// Query events using dynamic function (SELECT * + scanEvents)
	events, err := q.ListEventsForExport(ctx, EventFilterParams{CalendarID: calID})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(events) < 1 {
		t.Error("expected at least one event")
	}

	// Query todos using dynamic function (SELECT * + scanTodos)
	todos, err := q.ListTodosForExport(ctx, ListTodosForExportParams{CalendarID: calID})
	if err != nil {
		t.Fatalf("query todos: %v", err)
	}
	if len(todos) < 1 {
		t.Error("expected at least one todo")
	}

	// Query journals using dynamic function (SELECT * + scanJournals)
	journals, err := q.ListJournalsForExport(ctx, ListJournalsForExportParams{CalendarID: calID})
	if err != nil {
		t.Fatalf("query journals: %v", err)
	}
	if len(journals) < 1 {
		t.Error("expected at least one journal")
	}
}
