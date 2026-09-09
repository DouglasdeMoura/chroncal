package main

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
)

// sync.http_timeout tunes the CalDAV request ceiling. A slow server, for
// example iCloud, needs a long ceiling. A hung server needs a short one
// (issue #768).
func TestApplyCalDAVHTTPTimeout(t *testing.T) {
	previous := caldav.HTTPTimeout()
	t.Cleanup(func() { caldav.SetHTTPTimeout(previous) })

	applyCalDAVHTTPTimeout("90s")
	if got := caldav.HTTPTimeout(); got != 90*time.Second {
		t.Errorf("caldav.HTTPTimeout() = %s, want 1m30s", got)
	}

	// An empty value keeps whatever the package already uses.
	applyCalDAVHTTPTimeout("")
	if got := caldav.HTTPTimeout(); got != 90*time.Second {
		t.Errorf("caldav.HTTPTimeout() after an empty value = %s, want 1m30s", got)
	}
}

// An unusable value keeps the built-in default. It does not stop the command.
// This key affects the CalDAV requests only, and the root command applies it
// for every command, including a local one such as `chroncal event list`.
func TestApplyCalDAVHTTPTimeoutKeepsDefaultForBadValues(t *testing.T) {
	previous := caldav.HTTPTimeout()
	t.Cleanup(func() { caldav.SetHTTPTimeout(previous) })

	for _, raw := range []string{"soon", "5", "0s", "-1m"} {
		applyCalDAVHTTPTimeout(raw)
		if got := caldav.HTTPTimeout(); got != previous {
			t.Errorf("caldav.HTTPTimeout() after %q = %s, want the unchanged %s", raw, got, previous)
		}
	}
}

// A parent context must outlive one CalDAV request. A short parent deadline
// cut a slow first pull before the server answered (issue #768). The budget
// derives from the request timeout, so the rule must hold for a configured
// timeout too, not only for the default.
func TestSyncRunBudgetOutlivesOneRequest(t *testing.T) {
	previous := caldav.HTTPTimeout()
	t.Cleanup(func() { caldav.SetHTTPTimeout(previous) })

	for _, request := range []time.Duration{30 * time.Second, 5 * time.Minute, 45 * time.Minute} {
		caldav.SetHTTPTimeout(request)
		if got := syncRunBudget(); got <= request {
			t.Errorf("syncRunBudget() = %s at a request ceiling of %s, want more", got, request)
		}
	}
}
