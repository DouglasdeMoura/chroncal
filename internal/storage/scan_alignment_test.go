package storage

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestScanColumnAlignment opens a DB with all migrations applied. It inserts
// minimal rows into events, todos, and journals. It then calls the dynamic query
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

// TestExpectedColumnsMatchSchema pins each expected column list to the live
// schema order. SQLite returns SELECT * columns in migration order. A
// migration that adds, drops, or moves a column must update the matching
// list and the scanner together. This test fails until both change.
func TestExpectedColumnsMatchSchema(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cases := []struct {
		table string
		want  []string
	}{
		{"events", eventColumns},
		{"todos", todoColumns},
		{"journals", journalColumns},
	}
	for _, tc := range cases {
		rows, err := db.Query("SELECT name FROM pragma_table_info(?)", tc.table)
		if err != nil {
			t.Fatalf("table_info %s: %v", tc.table, err)
		}
		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan %s column name: %v", tc.table, err)
			}
			got = append(got, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s columns: %v", tc.table, err)
		}
		rows.Close()
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s schema columns %v do not match the expected list %v; update scan_helpers.go with the migration",
				tc.table, got, tc.want)
		}
	}
}

// TestQueryRejectsTransposedColumns proves the fail-fast guard. The test
// swaps two same-type columns through a view, so the row count and the
// column types stay valid. A plain scan would swap the two values in
// silence. The guard must return a descriptive error instead.
func TestQueryRejectsTransposedColumns(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	transposed := func(cols []string) string {
		swapped := slices.Clone(cols)
		// Index 3 and 4 hold title/summary and description in every list.
		// Both columns are TEXT in all three tables.
		swapped[3], swapped[4] = swapped[4], swapped[3]
		return strings.Join(swapped, ", ")
	}

	steps := []struct {
		table string
		cols  []string
		query func() error
	}{
		{"events", eventColumns, func() error {
			_, err := q.queryEvents(ctx, "WHERE 1=0", nil, "id")
			return err
		}},
		{"todos", todoColumns, func() error {
			_, err := q.queryTodos(ctx, "WHERE 1=0", nil, "id")
			return err
		}},
		{"journals", journalColumns, func() error {
			_, err := q.queryJournals(ctx, "WHERE 1=0", nil, "id")
			return err
		}},
	}
	for _, s := range steps {
		if _, err := db.Exec("ALTER TABLE " + s.table + " RENAME TO " + s.table + "_probe"); err != nil {
			t.Fatalf("rename %s: %v", s.table, err)
		}
		if _, err := db.Exec("CREATE VIEW " + s.table + " AS SELECT " + transposed(s.cols) + " FROM " + s.table + "_probe"); err != nil {
			t.Fatalf("create %s view: %v", s.table, err)
		}
		err := s.query()
		if err == nil {
			t.Fatalf("query through the transposed %s view returned no error", s.table)
		}
		if !strings.Contains(err.Error(), "scan expects") {
			t.Errorf("%s transposition error lacks the expected column list: %v", s.table, err)
		}
	}
}
