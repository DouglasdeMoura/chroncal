package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The TUI's sync completion line is the only sync surface the user sees, so
// a pull that fabricated values (import warnings on the SyncResult) must be
// counted there — silence would leave the fabrication invisible until it is
// pushed back over the server's correct value.
func TestSyncSummaryCountsImportWarnings(t *testing.T) {
	t.Parallel()

	if got := syncSummary("Work", 0, 3, 0, 0, 2); !strings.Contains(got, "2 import warnings") {
		t.Errorf("syncSummary with 2 warnings = %q, want it to count them", got)
	}
	if got := syncSummary("Work", 0, 3, 0, 0, 1); !strings.Contains(got, "1 import warning") || strings.Contains(got, "warnings") {
		t.Errorf("syncSummary with 1 warning = %q, want singular count", got)
	}
	if got := syncSummary("Work", 0, 3, 0, 0, 0); strings.Contains(got, "import warning") {
		t.Errorf("syncSummary with no warnings = %q, want no warning segment", got)
	}
}

// The TUI sync engine's logger must never write to stderr (Bubble Tea owns
// the terminal), but discarding it loses logImportWarnings' detail. Per the
// repo's logging contract the durable copy goes to the state-dir log file,
// same as the purge loop.
func TestNewStateDirSyncLoggerWritesToLogFile(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	logger := newStateDirSyncLogger()
	logger.Warn("import warning", "path", "/cal/warned.ics", "warning", "malformed DTEND")

	data, err := os.ReadFile(filepath.Join(stateHome, "chroncal", "chroncal.log"))
	if err != nil {
		t.Fatalf("read state-dir log: %v", err)
	}
	if !strings.Contains(string(data), "malformed DTEND") {
		t.Errorf("state-dir log missing the import warning; log:\n%s", data)
	}
}
