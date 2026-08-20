package caldav

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/retry"
)

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{name: "seconds", value: "30", want: 30 * time.Second, ok: true},
		{name: "zero seconds", value: "0", want: 0, ok: true},
		{name: "whitespace", value: "  15 ", want: 15 * time.Second, ok: true},
		{name: "http date future", value: now.Add(45 * time.Second).UTC().Format(http.TimeFormat), want: 45 * time.Second, ok: true},
		{name: "http date past", value: now.Add(-time.Minute).UTC().Format(http.TimeFormat), want: 0, ok: true},
		{name: "empty", value: "", want: 0, ok: false},
		{name: "negative", value: "-5", want: 0, ok: false},
		{name: "garbage", value: "soon", want: 0, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseRetryAfter(tt.value, now)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseRetryAfter(%q) = (%v, %v), want (%v, %v)", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestHTTPErrorThreadsRetryAfter(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Header:     http.Header{"Retry-After": {"30"}},
	}
	err := httpError(resp)

	if !retry.IsTransient(err) {
		t.Fatalf("httpError(429) not classified transient: %v", err)
	}
	var te *retry.TransientError
	if !errors.As(err, &te) {
		t.Fatalf("httpError(429 + Retry-After) = %T, want *retry.TransientError", err)
	}
	if te.RetryAfter != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s", te.RetryAfter)
	}
}

func TestHTTPErrorNoRetryAfterStaysPlain(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Status:     "503 Service Unavailable",
		Header:     http.Header{},
	}
	err := httpError(resp)

	var te *retry.TransientError
	if errors.As(err, &te) {
		t.Fatalf("httpError(503 without Retry-After) wrapped as TransientError unexpectedly")
	}
	// Still retryable via status-code classification.
	if !retry.IsTransient(err) {
		t.Fatalf("httpError(503) should still be transient: %v", err)
	}
}

func TestHTTPErrorAnnotatesGoogleCalDAVForbidden(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://apidata.googleusercontent.com/caldav/v2/me@example.com/events/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"reason":"accessNotConfigured"}}`)),
		Request:    req,
	}
	got := httpError(resp)
	if got == nil {
		t.Fatal("httpError = nil, want 403")
	}
	if !strings.Contains(got.Error(), "accessNotConfigured") {
		t.Fatalf("err = %q, want the response body", got)
	}
	if !strings.Contains(got.Error(), googleCalDAVForbiddenHint) {
		t.Fatalf("err = %q, want the caldav.googleapis.com hint", got)
	}
}
