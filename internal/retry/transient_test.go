package retry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

// TestTypedStatusNotShadowedByNumericPrefix guards against regressions of
// issue #134: when a wrapping layer prepends a numeric token (a batch
// index, a host:port segment), classification must still read the real
// HTTP status from the typed HTTPError rather than scraping the first
// 3-digit token out of the message.
func TestTypedStatusNotShadowedByNumericPrefix(t *testing.T) {
	t.Parallel()

	serverErr := fmt.Errorf("multiget batch 100: %w", NewHTTPError(503, errors.New("HTTP 503 Service Unavailable")))
	if !IsTransient(serverErr) {
		t.Fatalf("IsTransient(%v) = false, want true (wrapped 503 must be retried)", serverErr)
	}
	if IsConflict(serverErr) {
		t.Fatalf("IsConflict(%v) = true, want false (503 is not a conflict)", serverErr)
	}

	conflictErr := fmt.Errorf("PUT https://dav.example.com:100/cal: %w", NewHTTPError(412, errors.New("HTTP 412 Precondition Failed")))
	if !IsConflict(conflictErr) {
		t.Fatalf("IsConflict(%v) = false, want true (wrapped 412 must be detected)", conflictErr)
	}
	if IsTransient(conflictErr) {
		t.Fatalf("IsTransient(%v) = true, want false (412 must not be retried)", conflictErr)
	}
}

func TestIsTransient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "dns timeout",
			err:  &net.DNSError{IsTimeout: true},
			want: true,
		},
		{
			name: "rate limited http error",
			err:  errors.New("429 Too Many Requests"),
			want: true,
		},
		{
			name: "server error",
			err:  errors.New("503 Service Unavailable"),
			want: true,
		},
		{
			name: "bad request",
			err:  errors.New("400 Bad Request"),
			want: false,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := IsTransient(tt.err); got != tt.want {
				t.Fatalf("IsTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "precondition failed",
			err:  errors.New("412 Precondition Failed"),
			want: true,
		},
		{
			name: "http conflict",
			err:  errors.New("409 Conflict"),
			want: true,
		},
		{
			name: "server unavailable",
			err:  errors.New("503 Service Unavailable"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := IsConflict(tt.err); got != tt.want {
				t.Fatalf("IsConflict(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
