package sync

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
)

// SyncAccount gives each calendar its own deadline. That deadline must
// outlive one CalDAV request, or it cuts a slow first pull before the server
// answers (issue #768). The budget derives from the request timeout, so the
// rule must hold for a configured timeout too, not only for the default.
func TestAccountCalendarSyncBudgetOutlivesOneRequest(t *testing.T) {
	previous := caldav.HTTPTimeout()
	t.Cleanup(func() { caldav.SetHTTPTimeout(previous) })

	for _, request := range []time.Duration{30 * time.Second, 5 * time.Minute, 45 * time.Minute} {
		caldav.SetHTTPTimeout(request)
		if got := accountCalendarSyncBudget(); got <= request {
			t.Errorf("accountCalendarSyncBudget() = %s at a request ceiling of %s, want more", got, request)
		}
	}
}
