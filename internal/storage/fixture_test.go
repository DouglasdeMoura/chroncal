package storage

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The storage tests cannot import internal/testutil, because testutil
// imports storage. openFixtureDB gives the same template copy: the
// migration set runs once per test binary, and each test opens a copy.
var (
	fixtureOnce sync.Once
	fixturePath string
	fixtureErr  error
)

func buildFixtureDB() (string, error) {
	dir, err := os.MkdirTemp("", "chroncal-storage-fixture-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "fixture.db")
	conn, _, err := Open(path)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	// Open on each copy writes a new credential namespace and location.
	if _, err := conn.ExecContext(context.Background(), `
		DELETE FROM credential_locations;
		DELETE FROM credential_namespace;
	`); err != nil {
		conn.Close()
		os.RemoveAll(dir)
		return "", err
	}
	_, _ = conn.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := conn.Close(); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// TestMain removes the fixture directory after the tests of the package.
func TestMain(m *testing.M) {
	code := m.Run()
	if fixturePath != "" {
		_ = os.RemoveAll(filepath.Dir(fixturePath))
	}
	os.Exit(code)
}

// openFixtureDB opens a copy of the migrated fixture database. Use it in
// each storage test that does not test the migration set itself.
func openFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	fixtureOnce.Do(func() { fixturePath, fixtureErr = buildFixtureDB() })
	if fixtureErr != nil {
		t.Fatalf("build the fixture database: %v", fixtureErr)
	}
	dst := filepath.Join(t.TempDir(), "test.db")
	in, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open the fixture: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create the copy: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("copy the fixture: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close the copy: %v", err)
	}
	conn, _, err := Open(dst)
	if err != nil {
		t.Fatalf("open the copy: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
