package retry

import (
	"errors"
	"testing"
	"time"
)

// TestRetryAfterDelayCapped covers the cap on a server-requested
// Retry-After floor. A hostile or broken server can request an absurd
// delay. The retry caller often holds locks (the sync push lock is one
// example), so the floor must never exceed maxRetryAfterDelay.
func TestRetryAfterDelayCapped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want time.Duration
	}{
		{
			name: "no server hint",
			err:  errors.New("503 Service Unavailable"),
			want: 0,
		},
		{
			name: "hint below cap",
			err:  &TransientError{Err: errors.New("429 Too Many Requests"), RetryAfter: 30 * time.Second},
			want: 30 * time.Second,
		},
		{
			name: "hint at cap",
			err:  &TransientError{Err: errors.New("429 Too Many Requests"), RetryAfter: 60 * time.Second},
			want: 60 * time.Second,
		},
		{
			name: "hostile hint above cap",
			err:  &TransientError{Err: errors.New("429 Too Many Requests"), RetryAfter: 24 * time.Hour},
			want: 60 * time.Second,
		},
		{
			name: "negative hint ignored",
			err:  &TransientError{Err: errors.New("429 Too Many Requests"), RetryAfter: -time.Second},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := retryAfterDelay(tt.err); got != tt.want {
				t.Fatalf("retryAfterDelay() = %v, want %v", got, tt.want)
			}
		})
	}
}
