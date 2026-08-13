package tui

import (
	"log/slog"
	gosync "sync"

	"github.com/douglasdemoura/chroncal/internal/config"
)

// stateDirSyncLogger returns the shared logger for sync engines the TUI
// constructs. The TUI owns the terminal, so sync logs must never touch
// stderr; per the repo's logging contract they go to the state-dir log file
// via the shared config.StateDirLogger constructor (same file the purge
// loop's purgeLogger uses). This keeps logImportWarnings' full detail
// durable and inspectable while the in-UI surface shows only the warning
// count from SyncResult.Warnings. Opened once per process via OnceValue.
var stateDirSyncLogger = gosync.OnceValue(newStateDirSyncLogger)

func newStateDirSyncLogger() *slog.Logger {
	return config.StateDirLogger()
}
