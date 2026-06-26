package main

import (
	"strings"
	"testing"
)

// A typo'd / non-existent calendar name must fail loudly rather than
// silently exiting 0, so the user knows nothing was reset.
func TestSyncResetUnknownCalendarFails(t *testing.T) {
	setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	_, stderr, err := runChroncalCommand(t, "sync", "reset", "--calendar", "Wrok")
	if err == nil {
		t.Fatalf("sync reset with unknown calendar exited 0; want non-zero. stderr=%q", stderr)
	}
	if !strings.Contains(strings.ToLower(stderr), "not found") {
		t.Fatalf("sync reset stderr = %q, want a not-found message", stderr)
	}
}

// A calendar that exists but is local-only (not connected to a remote)
// has no sync state to reset; the command must say so instead of a
// silent no-op.
func TestSyncResetLocalOnlyCalendarReportsNotConnected(t *testing.T) {
	setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	_, stderr, err := runChroncalCommand(t, "sync", "reset", "--calendar", "Work")
	if err == nil {
		t.Fatalf("sync reset on local-only calendar exited 0; want non-zero. stderr=%q", stderr)
	}
	if !strings.Contains(strings.ToLower(stderr), "not connected") {
		t.Fatalf("sync reset stderr = %q, want a not-connected message", stderr)
	}
}
