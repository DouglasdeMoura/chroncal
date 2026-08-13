package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/config"
)

// The background purger must log to the state-dir file, not the
// terminal: the TUI owns the display, and silent discard would hide
// purge failures forever.
func TestPurgeLogger_WritesToStateDirFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	logger := purgeLogger()
	logger.Warn("soft-delete purge failed", "error", "boom")

	path, err := config.LogFilePath()
	if err != nil {
		t.Fatalf("LogFilePath: %v", err)
	}
	if want := filepath.Join(dir, "chroncal", "chroncal.log"); path != want {
		t.Fatalf("LogFilePath = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "soft-delete purge failed") {
		t.Errorf("log file missing warning, got: %q", string(data))
	}
}

// When the log file cannot be created, the logger must degrade to
// silence rather than stderr, which would print over the TUI.
func TestPurgeLogger_UnwritableStateDirFallsBackToSilence(t *testing.T) {
	dir := t.TempDir()
	// Occupy the chroncal state-dir path with a file so MkdirAll fails.
	if err := os.WriteFile(filepath.Join(dir, "chroncal"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", dir)

	logger := purgeLogger()
	logger.Warn("soft-delete purge failed", "error", "boom")

	if _, err := os.Stat(filepath.Join(dir, "chroncal", "chroncal.log")); err == nil {
		t.Fatal("log file should not exist when state dir is unwritable")
	}
	// DiscardHandler reports Enabled == false for every level; a handler
	// that says it would log means the fallback writes somewhere, which
	// is the bug this test guards.
	if logger.Handler().Enabled(t.Context(), slog.LevelError) {
		t.Error("fallback logger should be the discard handler")
	}
}
