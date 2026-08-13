package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

// SharedStateDirLogger is the memoized, process-wide StateDirLogger. Every
// long-lived background consumer (the purge loop, the TUI's sync engines)
// must use this one so the process holds a single append fd on the log file
// instead of one per constructor call. StateDirLogger stays exported
// unmemoized for tests, which point XDG_STATE_HOME at a fresh temp dir per
// case and need a fresh handle each time.
var SharedStateDirLogger = sync.OnceValue(StateDirLogger)
