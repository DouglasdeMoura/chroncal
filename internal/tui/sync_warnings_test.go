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

// The account-linking pulls (first import after discovery, and the manage-
// calendars reconcile) run the same first sync as every other TUI path, so
// import warnings collected on their SyncResults must reach the status line
// count like syncSummary does — not be silently dropped.
func TestAccountImportFinishedStatusCountsImportWarnings(t *testing.T) {
	t.Parallel()

	m := NewModel(nil, "")
	updated, _ := m.Update(accountImportFinishedMsg{created: 2, synced: 2, warnings: 3})
	m = updated.(Model)
	if !strings.Contains(m.syncStatus, "3 import warnings") {
		t.Errorf("account import status = %q, want the 3 import warnings counted", m.syncStatus)
	}

	// A partial first sync still surfaces the warnings the successful pulls
	// collected.
	m = NewModel(nil, "")
	updated, _ = m.Update(accountImportFinishedMsg{created: 2, synced: 1, syncErr: os.ErrDeadlineExceeded, warnings: 1})
	m = updated.(Model)
	if !strings.Contains(m.syncStatus, "1 import warning") || strings.Contains(m.syncStatus, "warnings") {
		t.Errorf("partial account import status = %q, want the single warning counted", m.syncStatus)
	}
}

func TestAccountSelectionFinishedStatusCountsImportWarnings(t *testing.T) {
	t.Parallel()

	m := NewModel(nil, "")
	updated, _ := m.Update(accountSelectionFinishedMsg{created: 1, synced: 1, warnings: 2})
	m = updated.(Model)
	if !strings.Contains(m.syncStatus, "2 import warnings") {
		t.Errorf("account selection status = %q, want the 2 import warnings counted", m.syncStatus)
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
