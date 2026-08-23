package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// TestProviderRegistersEveryGoMigration guards the manual registration in
// NewMigrationProvider. goose runs only the migrations that the
// goose.WithGoMigrations call lists. A new migration_NNN_*.go file that the
// call omits never runs, and no error reports the skip. The test reads the
// registered versions from the production provider and compares them with
// the Go migration files in this package. It also checks that the SQL and
// Go versions together form one gap-free range.
func TestProviderRegistersEveryGoMigration(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer conn.Close()

	registered := make(map[int64]bool)
	all := make(map[int64]struct{})
	var maxVersion int64
	for _, s := range migProvider(t, conn).ListSources() {
		all[s.Version] = struct{}{}
		if s.Version > maxVersion {
			maxVersion = s.Version
		}
		if s.Type == goose.TypeGo {
			registered[s.Version] = true
		}
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the test source file")
	}
	entries, err := os.ReadDir(filepath.Dir(thisFile))
	if err != nil {
		t.Fatalf("list the package directory: %v", err)
	}
	nameVersion := regexp.MustCompile(`^migration_(\d+)_`)
	sawGoFile := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		m := nameVersion.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		sawGoFile = true
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			t.Fatalf("parse the version in %s: %v", name, err)
		}
		if !registered[version] {
			t.Errorf("%s carries version %d, but NewMigrationProvider registers no Go migration for it; add the migration to goose.WithGoMigrations so it runs",
				name, version)
		}
	}
	if !sawGoFile {
		t.Fatal("find no migration_*.go file in the package; the naming convention changed")
	}

	for version := int64(1); version <= maxVersion; version++ {
		if _, ok := all[version]; !ok {
			t.Errorf("no SQL file and no registered Go migration carries version %d; the migration set has a gap", version)
		}
	}
}
