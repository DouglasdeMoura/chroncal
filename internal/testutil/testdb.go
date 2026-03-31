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
