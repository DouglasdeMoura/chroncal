package tui

import (
	"github.com/douglasdemoura/chroncal/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The TUI's sync completion line is the only sync surface the user sees. A
// pull that fabricated values (import warnings on the SyncResult) must be
// counted there. Silence would leave the fabrication invisible until it is
// pushed back over the server's correct value.
func TestSyncSummaryCountsImportWarnings(t *testing.T) {
	t.Parallel()

	if got := syncSummary("Work", syncTotals{pulled: 3, warnings: 2}); !strings.Contains(got, "2 import warnings") {
		t.Errorf("syncSummary with 2 warnings = %q, want it to count them", got)
	}
	if got := syncSummary("Work", syncTotals{pulled: 3, warnings: 1}); !strings.Contains(got, "1 import warning") || strings.Contains(got, "warnings") {
		t.Errorf("syncSummary with 1 warning = %q, want singular count", got)
	}
	if got := syncSummary("Work", syncTotals{pulled: 3}); strings.Contains(got, "import warning") {
		t.Errorf("syncSummary with no warnings = %q, want no warning segment", got)
	}
}

// The account-link pulls (first import after discovery, and the manage-
// calendars reconcile) run the same first sync as every other TUI path.
// Import warnings collected on their SyncResults must reach the status line
// count like syncSummary does. They must not be dropped in silence.
func TestAccountImportFinishedStatusCountsImportWarnings(t *testing.T) {
	// NOT t.Parallel(): NewModel writes the package-global theme via
	// SetActiveTheme, so tests that construct a Model must not run
	// concurrently — same convention as every other NewModel test here.
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
	// NOT t.Parallel(): NewModel writes the package-global theme via
	// SetActiveTheme, so tests that construct a Model must not run
	// concurrently — same convention as every other NewModel test here.
	m := NewModel(nil, "")
	updated, _ := m.Update(accountSelectionFinishedMsg{created: 1, synced: 1, warnings: 2})
	m = updated.(Model)
	if !strings.Contains(m.syncStatus, "2 import warnings") {
		t.Errorf("account selection status = %q, want the 2 import warnings counted", m.syncStatus)
	}
}

// The TUI sync engine's logger must never write to stderr (Bubble Tea owns
// the terminal). A discard of it loses logImportWarnings' detail. Per the
// repo's log contract the durable copy goes to the state-dir log file,
// same as the purge loop. The engine uses the memoized
// config.SharedStateDirLogger. The unmemoized constructor is asserted here
// so the fresh XDG_STATE_HOME takes effect.
func TestStateDirSyncLoggerWritesToLogFile(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	logger := config.StateDirLogger()
	logger.Warn("import warning", "path", "/cal/warned.ics", "warning", "malformed DTEND")

	data, err := os.ReadFile(filepath.Join(stateHome, "chroncal", "chroncal.log"))
	if err != nil {
		t.Fatalf("read state-dir log: %v", err)
	}
	if !strings.Contains(string(data), "malformed DTEND") {
		t.Errorf("state-dir log missing the import warning; log:\n%s", data)
	}
}
