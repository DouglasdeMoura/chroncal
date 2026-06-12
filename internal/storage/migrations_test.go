package storage

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"

	dbembed "github.com/douglasdemoura/chroncal/db"
)

// Verifies migration 031 rolls back and re-applies cleanly (table rebuild +
// trigger recreation in both directions).
func TestMigration031UpDown(t *testing.T) {
	conn, _, err := Open(t.TempDir() + "/mig.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	migrationsFS, err := fs.Sub(dbembed.Migrations, "migrations")
	if err != nil {
		t.Fatalf("sub fs: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationsFS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	ctx := context.Background()
	if _, err := provider.DownTo(ctx, 30); err != nil {
		t.Fatalf("down to 30: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}
}
