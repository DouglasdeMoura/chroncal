package storage

import (
	"context"
	"os"
	"testing"
	"time"
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

func TestCredentialScopes_StableAndDifferentiateDatabaseCopies(t *testing.T) {
	ctx := context.Background()
	firstPath := t.TempDir() + "/first.db"
	first, firstQueries, err := Open(firstPath)
	if err != nil {
		t.Fatalf("open first database: %v", err)
	}
	account, err := firstQueries.CreateAccount(ctx, CreateAccountParams{
		Name: "source", ServerUrl: "https://example.com", AuthType: "basic", Username: "alice",
	})
	if err != nil {
		t.Fatalf("create source account: %v", err)
	}
	if err := firstQueries.AdvanceCurrentCredentialAccountWatermark(ctx, account.ID); err != nil {
		t.Fatalf("advance source account watermark: %v", err)
	}
	firstScopes, err := GetCredentialScopes(ctx, first, firstPath)
	if err != nil {
		t.Fatalf("first scopes: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	reopened, _, err := Open(firstPath)
	if err != nil {
		t.Fatalf("reopen first database: %v", err)
	}
	reopenedScopes, err := GetCredentialScopes(ctx, reopened, firstPath)
	if err != nil {
		t.Fatalf("reopened scopes: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened database: %v", err)
	}
	if reopenedScopes.Current != firstScopes.Current {
		t.Fatalf("database scope changed across reopen: %q -> %q", firstScopes.Current, reopenedScopes.Current)
	}

	copiedPath := t.TempDir() + "/copied.db"
	data, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read source database: %v", err)
	}
	if err := os.WriteFile(copiedPath, data, 0o600); err != nil {
		t.Fatalf("copy database: %v", err)
	}
	copied, _, err := Open(copiedPath)
	if err != nil {
		t.Fatalf("open copied database: %v", err)
	}
	defer copied.Close()
	copiedScopes, err := GetCredentialScopes(ctx, copied, copiedPath)
	if err != nil {
		t.Fatalf("copied scopes: %v", err)
	}
	if copiedScopes.Current == firstScopes.Current {
		t.Fatalf("copied database reused source credential scope %q", firstScopes.Current)
	}
	foundSource := false
	for _, previous := range copiedScopes.Previous {
		foundSource = foundSource ||
			(previous.Namespace == firstScopes.Current && previous.MaxAccountID == account.ID)
	}
	if !foundSource {
		t.Fatalf("copied database cannot migrate source credentials: previous=%v want %q", copiedScopes.Previous, firstScopes.Current)
	}

	secondPath := t.TempDir() + "/second.db"
	second, _, err := Open(secondPath)
	if err != nil {
		t.Fatalf("open independent database: %v", err)
	}
	defer second.Close()
	secondScopes, err := GetCredentialScopes(ctx, second, secondPath)
	if err != nil {
		t.Fatalf("independent scopes: %v", err)
	}
	if secondScopes.Current == firstScopes.Current {
		t.Fatalf("independent databases share credential scope %q", firstScopes.Current)
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

// TestOpen_InMemorySchemaVisibleAcrossConnections guards issue #214: a plain
// ":memory:" DSN is a private per-connection database with modernc.org/sqlite.
// Migrations run on whichever connection the pool hands out first; without
// pinning the pool to a single connection, a second concurrent connection sees
// a brand-new, schema-less database and reads fail with "no such table".
func TestOpen_InMemorySchemaVisibleAcrossConnections(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Hold one pooled connection open inside a live transaction so the read
	// below is forced onto a different connection.
	txReady := make(chan struct{})
	txErr := make(chan error, 1)
	txDone := make(chan struct{})
	go func() {
		defer close(txDone)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			txErr <- err
			close(txReady)
			return
		}
		close(txReady)
		// Keep the connection busy long enough that the pool must use a
		// second connection for any concurrent query.
		time.Sleep(200 * time.Millisecond)
		_ = tx.Rollback()
	}()

	<-txReady
	select {
	case err := <-txErr:
		t.Fatalf("BeginTx: %v", err)
	default:
	}

	// Before the fix, an unpinned in-memory pool hands this query a brand-new,
	// schema-less connection and the read fails with "no such table". After
	// the fix the pool is pinned to a single connection for ":memory:", so the
	// read blocks until the transaction releases the connection and then
	// succeeds against the same schema.
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM calendars").Scan(&n); err != nil {
		t.Fatalf("read calendars on second connection: %v", err)
	}
	<-txDone
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
