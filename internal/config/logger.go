package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// StateDirLogger returns a logger that appends to the state-dir log file
// (LogFilePath). It is the one constructor behind every background job that
// can run while the TUI owns the terminal. Those jobs are the soft-delete
// purge loop and the TUI's sync engines. A write to stderr would print over the
// display. If the file cannot be opened it degrades to silence (never
// stderr). The file handle intentionally stays open for the process
// lifetime. The background goroutines outlive this call. The OS reclaims
// the fd on exit. The jobs write a few lines per day, so no rotation.
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
// must use this one. The process then holds a single append fd on the log
// file instead of one per constructor call. StateDirLogger stays exported
// unmemoized for tests. Those tests point XDG_STATE_HOME at a fresh temp dir
// per case and need a fresh handle each time.
var SharedStateDirLogger = sync.OnceValue(StateDirLogger)
