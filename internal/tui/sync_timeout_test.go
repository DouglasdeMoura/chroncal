package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
)

// A TUI sync and a TUI account discovery must outlive one CalDAV request. A
// short deadline cut a slow first pull before the server answered
// (issue #768). Each budget derives from the request timeout, so the rule
// must hold for a configured timeout too, not only for the default.
func TestTUIBudgetsOutliveOneRequest(t *testing.T) {
	previous := caldav.HTTPTimeout()
	t.Cleanup(func() { caldav.SetHTTPTimeout(previous) })

	for _, request := range []time.Duration{30 * time.Second, 5 * time.Minute, 45 * time.Minute} {
		caldav.SetHTTPTimeout(request)
		if got := syncCalendarBudget(); got <= request {
			t.Errorf("syncCalendarBudget() = %s at a request ceiling of %s, want more", got, request)
		}
		if got := accountDiscoveryBudget(); got <= request {
			t.Errorf("accountDiscoveryBudget() = %s at a request ceiling of %s, want more", got, request)
		}
		// An account pass wraps the per-calendar budget of the engine, so it
		// must be larger. Equal budgets let one wedged calendar consume the
		// whole pass.
		if syncAccountBudget() <= syncCalendarBudget() {
			t.Errorf("syncAccountBudget() = %s, want more than syncCalendarBudget() %s",
				syncAccountBudget(), syncCalendarBudget())
		}
		// caldavPushTimeout stays short on purpose. The opportunistic push is
		// best-effort, and the user waits for the next screen. A push that runs
		// out of time keeps the dirty flag, and the next sync retries it.
		if caldavPushTimeout >= syncCalendarBudget() {
			t.Errorf("caldavPushTimeout = %s, want less than syncCalendarBudget() %s",
				caldavPushTimeout, syncCalendarBudget())
		}
		// The interactive probe stays short, and it never exceeds one request.
		if got := calendarProbeBudget(); got > request {
			t.Errorf("calendarProbeBudget() = %s at a request ceiling of %s, want no more", got, request)
		}
	}
}
