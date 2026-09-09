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

	if err := applyCalDAVHTTPTimeout("90s"); err != nil {
		t.Fatalf("applyCalDAVHTTPTimeout: %v", err)
	}
	if got := caldav.HTTPTimeout(); got != 90*time.Second {
		t.Errorf("caldav.HTTPTimeout() = %s, want 1m30s", got)
	}

	// An empty value keeps whatever the package already uses.
	if err := applyCalDAVHTTPTimeout(""); err != nil {
		t.Fatalf("applyCalDAVHTTPTimeout with an empty value: %v", err)
	}
	if got := caldav.HTTPTimeout(); got != 90*time.Second {
		t.Errorf("caldav.HTTPTimeout() after an empty value = %s, want 1m30s", got)
	}
}

func TestApplyCalDAVHTTPTimeoutRejectsBadValues(t *testing.T) {
	previous := caldav.HTTPTimeout()
	t.Cleanup(func() { caldav.SetHTTPTimeout(previous) })

	for _, raw := range []string{"soon", "5", "0s", "-1m"} {
		if err := applyCalDAVHTTPTimeout(raw); err == nil {
			t.Errorf("applyCalDAVHTTPTimeout(%q) = nil, want an error", raw)
		}
	}
	if got := caldav.HTTPTimeout(); got != previous {
		t.Errorf("caldav.HTTPTimeout() = %s, want the unchanged %s", got, previous)
	}
}

// A parent context must outlive one CalDAV request. A short parent deadline
// cut a slow first pull before the server answered (issue #768).
func TestSyncRunTimeoutOutlivesOneRequest(t *testing.T) {
	if syncRunTimeout <= caldav.HTTPTimeout() {
		t.Errorf("syncRunTimeout = %s, want more than the CalDAV request ceiling %s",
			syncRunTimeout, caldav.HTTPTimeout())
	}
}
