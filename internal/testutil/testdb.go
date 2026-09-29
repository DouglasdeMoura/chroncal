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
		os.RemoveAll(dir)
		return "", err
	}
	// Remove the credential namespace and the location that the template
	// recorded for its own file. storage.Open on each copy then writes a
	// new namespace UUID and the location of that copy, as it does for a
	// fresh database. Two test databases then never share a credential
	// scope, and no copy looks like a move from the template path.
	if _, err := db.ExecContext(context.Background(), `
		DELETE FROM credential_locations;
		DELETE FROM credential_namespace;
	`); err != nil {
		db.Close()
		os.RemoveAll(dir)
		return "", err
	}
	// Fold the WAL into the main file so a plain file copy carries the
	// whole schema.
	_, _ = db.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := db.Close(); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	// Success keeps the template directory for the process lifetime.
	// NewTestDB copies the template file on every call.
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

// copyTemplateDB copies the template database into a fresh test-owned path
// and returns that path. The first call builds the template.
func copyTemplateDB(t *testing.T) string {
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
	return dst
}

// NewTestDB creates a fresh file-backed SQLite database with all migrations
// applied. The first call builds a template database once. Each call copies
// that template, so the migration set does not run again. Each test gets an
// isolated database at its own path. The pool has a single connection, as
// storage.Open gives for ":memory:". A cleanup closes the database when the
// test ends.
func NewTestDB(t *testing.T) (*sql.DB, *storage.Queries) {
	t.Helper()
	db, q, err := storage.Open(copyTemplateDB(t))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// Pin the pool to one connection. Leak guards rely on a leaked
	// transaction that blocks the next query on the only connection.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db, q
}

// DBPath returns the path of a fresh database file with all migrations
// applied. Use it when the test does not open the database itself. The CLI
// tests point child processes at CHRONCAL_DB, and each child then opens the
// same migrated file. The first storage.Open of the file writes its credential
// namespace, as for a fresh database.
func DBPath(t *testing.T) string {
	t.Helper()
	return copyTemplateDB(t)
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
