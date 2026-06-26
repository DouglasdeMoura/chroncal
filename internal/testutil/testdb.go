package testutil

import (
	"database/sql"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// NewTestDB creates a fresh in-memory SQLite database with all migrations
// applied. The database is automatically closed when the test ends.
func NewTestDB(t *testing.T) (*sql.DB, *storage.Queries) {
	t.Helper()
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, q
}

// LinkCalendarToAccount creates an account and links calendar id 1 to it,
// turning calendar 1 into a synced calendar so storage.MarkResourceDirty
// (and related sync bookkeeping) actually writes sync_resources rows.
func LinkCalendarToAccount(t *testing.T, db *sql.DB) {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO accounts (name, server_url) VALUES ('Test', 'https://dav.example')`,
	)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	accID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("account id: %v", err)
	}
	if _, err := db.Exec(`UPDATE calendars SET account_id = ? WHERE id = 1`, accID); err != nil {
		t.Fatalf("link calendar: %v", err)
	}
}
