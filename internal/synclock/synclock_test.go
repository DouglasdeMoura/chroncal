package synclock

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

func TestAccountSerializesIndependentHandlesAcrossCacheEnvironments(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "first-cache"))
	dbPath := filepath.Join(t.TempDir(), "chroncal.db")
	db1, _, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open first database handle: %v", err)
	}
	defer db1.Close()
	db2, _, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open second database handle: %v", err)
	}
	defer db2.Close()

	releaseFirst, err := Account(context.Background(), db1, 7)
	if err != nil {
		t.Fatalf("lock first handle: %v", err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "second-cache"))

	acquiredSecond := make(chan func(), 1)
	errSecond := make(chan error, 1)
	go func() {
		release, err := Account(context.Background(), db2, 7)
		if err != nil {
			errSecond <- err
			return
		}
		acquiredSecond <- release
	}()

	select {
	case release := <-acquiredSecond:
		release()
		t.Fatal("second database handle acquired the same account lock early")
	case err := <-errSecond:
		t.Fatalf("lock second handle: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	releaseFirst()
	select {
	case release := <-acquiredSecond:
		release()
	case err := <-errSecond:
		t.Fatalf("lock second handle after release: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("second database handle did not acquire the released account lock")
	}
}

func TestAccountSerializesHardLinkedDatabaseHandles(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "chroncal.db")
	seed, _, err := storage.Open(originalPath)
	if err != nil {
		t.Fatalf("seed database: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	hardLinkPath := filepath.Join(dir, "chroncal-hardlink.db")
	if err := os.Link(originalPath, hardLinkPath); err != nil {
		t.Fatalf("create database hard link: %v", err)
	}

	db1, err := sql.Open("sqlite", originalPath)
	if err != nil {
		t.Fatalf("open original path: %v", err)
	}
	defer db1.Close()
	db2, err := sql.Open("sqlite", hardLinkPath)
	if err != nil {
		t.Fatalf("open hard-link path: %v", err)
	}
	defer db2.Close()

	releaseFirst, err := Account(context.Background(), db1, 9)
	if err != nil {
		t.Fatalf("lock original path: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if release, err := Account(ctx, db2, 9); err == nil {
		release()
		t.Fatal("hard-link path acquired the same account lock early")
	}
	releaseFirst()
	releaseSecond, err := Account(context.Background(), db2, 9)
	if err != nil {
		t.Fatalf("lock hard-link path after release: %v", err)
	}
	releaseSecond()
}
