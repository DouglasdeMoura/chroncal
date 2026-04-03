package main

import (
	"testing"
	"time"
)

func TestSyncDue(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		lastAttempt string
		interval    time.Duration
		want        bool
	}{
		{name: "disabled interval", lastAttempt: "", interval: 0, want: false},
		{name: "never synced", lastAttempt: "", interval: 15 * time.Minute, want: true},
		{name: "due", lastAttempt: "2026-04-03T11:30:00Z", interval: 15 * time.Minute, want: true},
		{name: "not due", lastAttempt: "2026-04-03T11:50:00Z", interval: 15 * time.Minute, want: false},
		{name: "invalid timestamp treated as due", lastAttempt: "not-a-time", interval: 15 * time.Minute, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := syncDue(now, tt.lastAttempt, tt.interval); got != tt.want {
				t.Fatalf("syncDue(%q, %v) = %v, want %v", tt.lastAttempt, tt.interval, got, tt.want)
			}
		})
	}
}
