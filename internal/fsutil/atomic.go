// Package fsutil holds small file system helpers that more than one
// package uses.
package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces the file at path with data. The function writes
// data to a temporary file in the same directory, then renames that file
// over path. A reader then never sees a partial file, and a crash or a full
// disk keeps the old file. tmpPattern is the os.CreateTemp pattern of the
// temporary file.
//
// The function:
//   - creates the parent directory with mode 0700 when it is not there
//   - sets perm on every write, so the mode of an old file does not stay
//   - syncs the data before the rename, so a power loss cannot keep the
//     rename and lose the data blocks
//   - syncs the parent directory after the rename, so the rename survives
//     a power loss
func WriteFileAtomic(path string, data []byte, perm fs.FileMode, tmpPattern string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("set the file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	// Some platforms reject fsync on a directory. The data file is already
	// synced. The loss of the directory entry on a crash is the risk that
	// remains, so ignore that failure.
	_ = d.Sync()
	d.Close()
	return nil
}
