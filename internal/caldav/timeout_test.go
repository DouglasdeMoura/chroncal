package caldav

import (
	"testing"
	"time"
)

// iCloud takes 30 to 35 seconds to answer a full-calendar multiget. The
// former 30-second ceiling killed every pull before the headers arrived
// (issue #768). The default must stay well above that observed time.
func TestDefaultHTTPTimeoutCoversASlowMultiget(t *testing.T) {
	if defaultHTTPTimeout < time.Minute {
		t.Errorf("defaultHTTPTimeout = %s, want at least 1m", defaultHTTPTimeout)
	}
	if got := HTTPTimeout(); got != defaultHTTPTimeout {
		t.Errorf("HTTPTimeout() = %s, want %s", got, defaultHTTPTimeout)
	}
}

func TestSetHTTPTimeout(t *testing.T) {
	previous := HTTPTimeout()
	t.Cleanup(func() { defaultHTTPClient.Timeout = previous })

	SetHTTPTimeout(90 * time.Second)
	if got := HTTPTimeout(); got != 90*time.Second {
		t.Errorf("HTTPTimeout() = %s, want 1m30s", got)
	}

	// A zero or negative value keeps the current timeout. The caller then
	// cannot turn the ceiling off by accident.
	SetHTTPTimeout(0)
	if got := HTTPTimeout(); got != 90*time.Second {
		t.Errorf("HTTPTimeout() after a zero value = %s, want 1m30s", got)
	}
	SetHTTPTimeout(-time.Second)
	if got := HTTPTimeout(); got != 90*time.Second {
		t.Errorf("HTTPTimeout() after a negative value = %s, want 1m30s", got)
	}
}

// The clients share one *http.Client, so a startup change of the timeout
// reaches a client that a constructor already built.
func TestSetHTTPTimeoutReachesABuiltClient(t *testing.T) {
	previous := HTTPTimeout()
	t.Cleanup(func() { defaultHTTPClient.Timeout = previous })

	if _, err := NewBasicAuthClient("https://example.com", "user", "pass"); err != nil {
		t.Fatalf("NewBasicAuthClient: %v", err)
	}
	SetHTTPTimeout(2 * time.Minute)
	if got := defaultHTTPClient.Timeout; got != 2*time.Minute {
		t.Errorf("shared client timeout = %s, want 2m", got)
	}
}
