//go:build unix

package icaltransfer_test

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
)

func parsePathWithTimeout(t *testing.T, path string) (icaltransfer.Preview, error) {
	t.Helper()
	type outcome struct {
		preview icaltransfer.Preview
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		preview, err := icaltransfer.ParsePath(path)
		done <- outcome{preview, err}
	}()
	select {
	case got := <-done:
		return got.preview, got.err
	case <-time.After(2 * time.Second):
		t.Fatal("ParsePath blocked on a FIFO")
		return icaltransfer.Preview{}, nil
	}
}

func mustMkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}
}

// TestParsePath_DirectorySkipsFIFO confirms a FIFO named .ics is skipped,
// so the import cannot block on a pipe.
func TestParsePath_DirectorySkipsFIFO(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "good.ics"), eventICS("fifo-good", "Good"))
	mustMkfifo(t, filepath.Join(root, "pipe.ics"))

	preview, err := parsePathWithTimeout(t, root)
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	if preview.Events != 1 {
		t.Fatalf("events = %d, want 1", preview.Events)
	}
}

// TestParsePath_RootFIFOIsAnError confirms a FIFO at the import path is an
// error, so the import cannot block on a pipe.
func TestParsePath_RootFIFOIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe.ics")
	mustMkfifo(t, path)

	_, err := parsePathWithTimeout(t, path)
	if err == nil || !strings.Contains(err.Error(), "not a regular file or directory") {
		t.Fatalf("err = %v, want a non-regular-file error", err)
	}
}
