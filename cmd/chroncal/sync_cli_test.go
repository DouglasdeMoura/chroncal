package main

import (
	"strings"
	"testing"
)

// TestSyncResetMatchesCalendarCaseInsensitively guards against issue #112:
// `sync reset --calendar work` must match a calendar named "Work" the same
// way every other command's --calendar flag does (case-insensitive,
// strings.EqualFold). Before the fix the reset loop compared names with ==,
// so a case-mismatched name silently matched nothing.
func TestSyncResetMatchesCalendarCaseInsensitively(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	stdout, stderr, err := runChroncalCommand(t, "sync", "reset", "--calendar", "work")
	if err != nil {
		t.Fatalf("sync reset --calendar work: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, "Reset sync state") {
		t.Fatalf("sync reset --calendar work did not reset the %q calendar; stdout = %q", "Work", stdout)
	}
}

// TestSyncRunMatchesCalendarCaseInsensitively guards against issue #112:
// `sync run --calendar work` must resolve a calendar named "Work"
// case-insensitively. Before the fix the run loop compared names with ==,
// so it reported `calendar "work" not found`.
func TestSyncRunMatchesCalendarCaseInsensitively(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	// The run will still fail downstream (no stored credentials), but it must
	// not fail with the case-sensitive "not found" resolution error.
	_, stderr, err := runChroncalCommand(t, "sync", "run", "--calendar", "work")
	if err == nil {
		return // resolved and ran; resolution is clearly case-insensitive
	}
	if strings.Contains(stderr, `calendar "work" not found`) {
		t.Fatalf("sync run --calendar work failed to resolve %q case-insensitively; stderr = %q", "Work", stderr)
	}
}
