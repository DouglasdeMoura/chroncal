package storage

import (
	"context"
	"strings"
	"testing"
)

func TestTodosSchemaRejectsDueDateAndDuration(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO todos (uid, calendar_id, summary, due_date, duration, status, priority, sequence)
		 VALUES ('todo-invalid-due-duration', ?, 'Invalid Todo', '2026-04-01', 'PT1H', 'NEEDS-ACTION', 0, 0)`,
		calID,
	)
	if err == nil {
		t.Fatal("expected due_date + duration insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTodosSchemaRejectsDurationWithoutStartDate(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO todos (uid, calendar_id, summary, duration, status, priority, sequence)
		 VALUES ('todo-invalid-duration-no-start', ?, 'Invalid Todo', 'PT1H', 'NEEDS-ACTION', 0, 0)`,
		calID,
	)
	if err == nil {
		t.Fatal("expected duration without start_date insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestXPropertiesRequireExistingOwner(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(
		`INSERT INTO x_properties (owner_type, owner_id, name, value, params)
		 VALUES ('event', 999, 'X-TEST', 'value', '{}')`,
	)
	if err == nil {
		t.Fatal("expected x_properties insert without owner to fail")
	}
}

func TestXPropertiesAreDeletedWithOwners(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	testCases := []struct {
		name      string
		ownerType string
		insertSQL string
		deleteSQL string
	}{
		{
			name:      "event",
			ownerType: "event",
			insertSQL: `INSERT INTO events (uid, calendar_id, title, start_time, end_time, all_day, status, transp, sequence, priority)
			           VALUES ('xprop-event', ?, 'Test', '2026-04-01T00:00:00Z', '2026-04-01T01:00:00Z', 0, 'CONFIRMED', 'OPAQUE', 0, 0)`,
			deleteSQL: `DELETE FROM events WHERE id = ?`,
		},
		{
			name:      "todo",
			ownerType: "todo",
			insertSQL: `INSERT INTO todos (uid, calendar_id, summary, status, priority, sequence)
			           VALUES ('xprop-todo', ?, 'Test', 'NEEDS-ACTION', 0, 0)`,
			deleteSQL: `DELETE FROM todos WHERE id = ?`,
		},
		{
			name:      "journal",
			ownerType: "journal",
			insertSQL: `INSERT INTO journals (uid, calendar_id, summary, status, sequence)
			           VALUES ('xprop-journal', ?, 'Test', 'FINAL', 0)`,
			deleteSQL: `DELETE FROM journals WHERE id = ?`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := db.Exec(tc.insertSQL, calID)
			if err != nil {
				t.Fatalf("insert owner: %v", err)
			}
			ownerID, err := res.LastInsertId()
			if err != nil {
				t.Fatalf("last insert id: %v", err)
			}

			if _, err := db.Exec(
				`INSERT INTO x_properties (owner_type, owner_id, name, value, params)
				 VALUES (?, ?, 'X-TEST', 'value', '{}')`,
				tc.ownerType, ownerID,
			); err != nil {
				t.Fatalf("insert x_property: %v", err)
			}

			if _, err := db.Exec(tc.deleteSQL, ownerID); err != nil {
				t.Fatalf("delete owner: %v", err)
			}

			var count int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM x_properties WHERE owner_type = ? AND owner_id = ?`,
				tc.ownerType, ownerID,
			).Scan(&count); err != nil {
				t.Fatalf("count x_properties: %v", err)
			}
			if count != 0 {
				t.Fatalf("x_properties count = %d, want 0", count)
			}
		})
	}
}

func TestSyncResourcesRejectInvalidOwnerType(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		 VALUES (?, 'sync-invalid-owner', 'note', '', '', 0, 'sync-token')`,
		calID,
	)
	if err == nil {
		t.Fatal("expected invalid owner_type insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSyncResourcesRejectInvalidDirtyValue(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		 VALUES (?, 'sync-invalid-dirty', 'event', '', '', 2, 'sync-token')`,
		calID,
	)
	if err == nil {
		t.Fatal("expected invalid dirty insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSyncResourcesRejectInvalidSyncStrategy(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		 VALUES (?, 'sync-invalid-strategy', 'event', '', '', 0, 'manual')`,
		calID,
	)
	if err == nil {
		t.Fatal("expected invalid sync_strategy insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSyncConflictsRejectInvalidOwnerType(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag)
		 VALUES (?, 'note', 1, 'conflict-invalid-owner', 'BEGIN:VCALENDAR', 'BEGIN:VCALENDAR', 'etag')`,
		calID,
	)
	if err == nil {
		t.Fatal("expected invalid sync_conflicts owner_type insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCalendarsAllowDuplicateDisplayNames(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	for range 2 {
		if _, err := db.Exec(
			`INSERT INTO calendars (name, color) VALUES ('Holidays in Brazil', '#7C3AED')`,
		); err != nil {
			t.Fatalf("insert duplicate display name: %v", err)
		}
	}
}

func TestCalendarsScopeRemoteIdentityToAccount(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	for _, name := range []string{"Personal", "Family"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO accounts (name, server_url) VALUES (?, 'https://cal.example.test/')`,
			name,
		); err != nil {
			t.Fatalf("insert account %q: %v", name, err)
		}
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO calendars (name, account_id, remote_url)
		 VALUES ('Work', 1, '/calendars/work/')`,
	); err != nil {
		t.Fatalf("insert first remote calendar: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO calendars (name, account_id, remote_url)
		 VALUES ('Work', 2, '/calendars/work/')`,
	); err != nil {
		t.Fatalf("same remote URL on another account should be allowed: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO calendars (name, account_id, remote_url)
		 VALUES ('Duplicate', 1, '/calendars/work/')`,
	); err == nil {
		t.Fatal("duplicate remote URL on one account should fail")
	}
}

func TestCalendarsRejectInvalidRemoteMetadata(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO calendars (name, remote_access) VALUES ('Bad access', 'admin')`,
	); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("invalid remote access error = %v, want CHECK constraint failure", err)
	}
	if _, err := db.Exec(
		`INSERT INTO calendars (name, remote_missing) VALUES ('Bad missing flag', 2)`,
	); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("invalid remote missing error = %v, want CHECK constraint failure", err)
	}
}
