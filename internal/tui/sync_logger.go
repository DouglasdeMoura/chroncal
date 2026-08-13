package tui

import (
	"log/slog"
	"os"
	"path/filepath"
	gosync "sync"

	"github.com/douglasdemoura/chroncal/internal/config"
)

// stateDirSyncLogger returns the shared logger for sync engines the TUI
// constructs. The TUI owns the terminal, so sync logs must never touch
// stderr; per the repo's logging contract they go to the state-dir log file
// (same pattern as the purge loop's purgeLogger in cmd/chroncal/main.go).
// This keeps logImportWarnings' full detail durable and inspectable while
// the in-UI surface shows only the warning count from SyncResult.Warnings.
// Opened once; the file handle intentionally lives for the process lifetime.
// If the file cannot be opened, fall back to silence — never stderr.
var stateDirSyncLogger = gosync.OnceValue(newStateDirSyncLogger)

func newStateDirSyncLogger() *slog.Logger {
	path, err := config.LogFilePath()
	if err != nil {
		return slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return slog.New(slog.DiscardHandler)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(slog.NewTextHandler(f, nil))
}
