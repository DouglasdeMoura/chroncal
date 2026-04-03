package caldav

import (
	"context"
	"errors"
	"net"
	"testing"
)

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
