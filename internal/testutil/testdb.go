package testutil

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// The template database holds the full schema. NewTestDB copies it per test,
// so the migration set runs once per test binary instead of once per test.
// Race instrumentation multiplies the migration cost, and that cost put
// several packages within seconds of the go test timeout (issue #569).
var (
	templateOnce sync.Once
	templatePath string
	templateErr  error
)

func buildTemplateDB() (string, error) {
	dir, err := os.MkdirTemp("", "chroncal-testdb-template-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "template.db")
	db, _, err := storage.Open(path)
	if err != nil {
		return "", err
	}
	// Fold the WAL into the main file so a plain file copy carries the
	// whole schema.
	_, _ = db.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := db.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// copyFile performs a byte-for-byte copy of one file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// NewTestDB creates a fresh file-backed SQLite database with all migrations
// applied. The first call builds a template database once, and every call
// afterwards copies that template instead of re-running the migration set.
// Each test still gets an isolated database at its own path. The database is
// automatically closed when the test ends.
func NewTestDB(t *testing.T) (*sql.DB, *storage.Queries) {
	t.Helper()
	templateOnce.Do(func() {
		templatePath, templateErr = buildTemplateDB()
	})
	if templateErr != nil {
		t.Fatalf("build template db: %v", templateErr)
	}
	dst := filepath.Join(t.TempDir(), "chroncal.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := templatePath + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, dst+suffix); err != nil {
			t.Fatalf("copy template db: %v", err)
		}
	}
	db, q, err := storage.Open(dst)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, q
}

// LinkCalendarToAccount creates an account and links calendar id 1 to it.
// Calendar 1 then becomes a synced calendar. storage.MarkResourceDirty
// (and related sync records) actually writes sync_resources rows.
func LinkCalendarToAccount(t *testing.T, db *sql.DB) {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO accounts (name, server_url) VALUES ('Test', 'https://dav.example')`,
	)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	accID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("account id: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE calendars SET account_id = ? WHERE id = 1`, accID); err != nil {
		t.Fatalf("link calendar: %v", err)
	}
}
