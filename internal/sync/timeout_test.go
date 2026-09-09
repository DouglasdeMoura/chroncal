package sync

import (
	"testing"

	"github.com/douglasdemoura/chroncal/internal/caldav"
)

// SyncAccount gives each calendar its own deadline. That deadline must
// outlive one CalDAV request, or it cuts a slow first pull before the server
// answers (issue #768).
func TestAccountCalendarSyncTimeoutOutlivesOneRequest(t *testing.T) {
	if accountCalendarSyncTimeout <= caldav.HTTPTimeout() {
		t.Errorf("accountCalendarSyncTimeout = %s, want more than the CalDAV request ceiling %s",
			accountCalendarSyncTimeout, caldav.HTTPTimeout())
	}
}
