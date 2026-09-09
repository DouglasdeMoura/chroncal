package tui

import (
	"testing"

	"github.com/douglasdemoura/chroncal/internal/caldav"
)

// A TUI sync and a TUI account discovery must outlive one CalDAV request. A
// short deadline cut a slow first pull before the server answered
// (issue #768).
func TestTUITimeoutsOutliveOneRequest(t *testing.T) {
	ceiling := caldav.HTTPTimeout()
	if syncOperationTimeout <= ceiling {
		t.Errorf("syncOperationTimeout = %s, want more than the CalDAV request ceiling %s",
			syncOperationTimeout, ceiling)
	}
	if accountDiscoveryTimeout <= ceiling {
		t.Errorf("accountDiscoveryTimeout = %s, want more than the CalDAV request ceiling %s",
			accountDiscoveryTimeout, ceiling)
	}
	// caldavPushTimeout stays short on purpose. The opportunistic push is
	// best-effort, and the user waits for the next screen. A push that runs
	// out of time keeps the dirty flag, and the next sync retries it.
	if caldavPushTimeout >= syncOperationTimeout {
		t.Errorf("caldavPushTimeout = %s, want less than syncOperationTimeout %s",
			caldavPushTimeout, syncOperationTimeout)
	}
}
