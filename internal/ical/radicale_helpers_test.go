package ical

import (
	"errors"
	"net/http"
	"testing"
)

func TestRadicaleURLFrom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty uses default", in: "", want: defaultRadicaleURL},
		{name: "whitespace uses default", in: "   ", want: defaultRadicaleURL},
		{name: "override", in: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "trims space and slash", in: " http://127.0.0.1:8080/ ", want: "http://127.0.0.1:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := radicaleURLFrom(tt.in); got != tt.want {
				t.Fatalf("radicaleURLFrom(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestShouldSkipRadicale(t *testing.T) {
	t.Parallel()
	connErr := errors.New("connection refused")
	tests := []struct {
		name   string
		err    error
		status int
		want   bool
	}{
		{name: "connection error", err: connErr, want: true},
		{name: "unauthorized", status: http.StatusUnauthorized, want: true},
		{name: "forbidden", status: http.StatusForbidden, want: true},
		{name: "ok", status: http.StatusOK, want: false},
		{name: "not found is reachable", status: http.StatusNotFound, want: false},
		{name: "method not allowed is reachable", status: http.StatusMethodNotAllowed, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldSkipRadicale(tt.err, tt.status); got != tt.want {
				t.Fatalf("shouldSkipRadicale(%v, %d) = %v, want %v", tt.err, tt.status, got, tt.want)
			}
		})
	}
}
