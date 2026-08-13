package config

import (
	"log/slog"
	"os"
	"path/filepath"
)

// StateDirLogger returns a logger appending to the state-dir log file
// (LogFilePath). It is the one constructor behind every background job that
// can run while the TUI owns the terminal — the soft-delete purge loop and
// the TUI's sync engines — where writing to stderr would print over the
// display. If the file cannot be opened it degrades to silence (never
// stderr). The file handle intentionally stays open for the process
// lifetime: the background goroutines outlive this call and the OS reclaims
// the fd on exit; the jobs write a few lines per day, so no rotation.
func StateDirLogger() *slog.Logger {
	path, err := LogFilePath()
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
