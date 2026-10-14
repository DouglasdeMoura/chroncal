package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicReplacesFileAndSetsMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, "file.txt")

	if err := WriteFileAtomic(path, []byte("one"), 0o644, ".file-*"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o600, ".file-*"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Fatalf("content = %q, want two", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries, want only the file", len(entries))
	}
}

// TestWriteFileAtomicIgnoresDirectorySyncFailure writes into a directory
// that the process can write and search but not read. The rename works,
// and the open of the directory for the sync fails. The file holds the new
// data, so the write must succeed.
func TestWriteFileAtomicIgnoresDirectorySyncFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	dir := filepath.Join(t.TempDir(), "noread")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "config.toml")

	if err := WriteFileAtomic(path, []byte("new"), 0o600, ".tmp-*"); err != nil {
		t.Fatalf("WriteFileAtomic = %v, want nil after the rename", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("file = %q, %v; want the new data", got, err)
	}
}
