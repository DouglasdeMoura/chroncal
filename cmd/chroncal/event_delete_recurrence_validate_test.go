package main

import (
	"strings"
	"testing"
)

// TestEventDelete_RecurrenceIDMustMatchAnOccurrence guards issue #745: a
// --recurrence-id that matches no generated instance used to write a phantom
// EXDATE and exit 0 while the real occurrence survived. The command must now
// fail with invalid_input, name the given timestamp, and point at the next
// valid occurrence.
func TestEventDelete_RecurrenceIDMustMatchAnOccurrence(t *testing.T) {
	t.Setenv("TZ", "UTC")
	dbPath := setupCalendarCLITestEnv(t)
	master := addOverrideGapSeries(t)

	const bogus = "2026-09-01T11:00:00Z"
	_, stderr, err := runChroncalCommand(t,
		"event", "delete", master.UID, "--recurrence-id", bogus, "--yes")
	if err == nil {
		t.Fatal("delete accepted a recurrence-id that matches no occurrence")
	}
	if !strings.Contains(stderr, "no occurrence of") || !strings.Contains(stderr, bogus) {
		t.Fatalf("stderr = %q, want it to name the timestamp and the series", stderr)
	}
	if !strings.Contains(stderr, "next occurrence is 2026-09-02T10:00:00Z") {
		t.Fatalf("stderr = %q, want it to name the next valid occurrence", stderr)
	}

	// The master row must be untouched: no phantom EXDATE.
	a := openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Events.GetByUID(t.Context(), master.UID)
	if err != nil {
		t.Fatalf("master vanished: %v", err)
	}
	for _, ex := range strings.Split(got.ExDates, ",") {
		if strings.TrimSpace(ex) == bogus {
			t.Fatalf("phantom EXDATE %q was written", bogus)
		}
	}
}

// TestEventDelete_FollowingMustRemoveSomething extends the guard to the
// truncation scope: a boundary after the last instance is a silent no-op and
// must be rejected.
func TestEventDelete_FollowingMustRemoveSomething(t *testing.T) {
	t.Setenv("TZ", "UTC")
	setupCalendarCLITestEnv(t)
	master := addOverrideGapSeries(t)

	_, stderr, err := runChroncalCommand(t,
		"event", "delete", master.UID, "--following", "2027-06-01T00:00:00Z", "--yes")
	if err == nil {
		t.Fatal("--following past the last instance exited 0")
	}
	if !strings.Contains(stderr, "no occurrence of") {
		t.Fatalf("stderr = %q, want the no-occurrence error", stderr)
	}
}

// TestEventDelete_MovedOverrideOriginalSlotSucceeds locks the raw-set
// semantics: a moved override keeps its original RECURRENCE-ID as the delete
// key. Deleting at the original slot must remove the occurrence, while
// deleting at the moved display start must be rejected without writing a
// phantom EXDATE.
func TestEventDelete_MovedOverrideOriginalSlotSucceeds(t *testing.T) {
	t.Setenv("TZ", "UTC")
	dbPath := setupCalendarCLITestEnv(t)
	master := addOverrideGapSeries(t) // daily COUNT=3 from 2026-09-01T10:00Z

	const orig = "2026-09-02T10:00:00Z"
	if _, _, err := runChroncalCommand(t, "event", "update", master.UID,
		"--recurrence-id", orig, "--title", "Moved slot"); err != nil {
		t.Fatalf("create override: %v", err)
	}

	// The moved display start is not a deletion key.
	_, stderr, err := runChroncalCommand(t,
		"event", "delete", master.UID, "--recurrence-id", "2026-09-02T13:00:00Z", "--yes")
	if err == nil {
		t.Fatal("delete at the moved time must fail")
	}
	if !strings.Contains(stderr, "no occurrence of") {
		t.Fatalf("stderr = %q, want the no-occurrence error", stderr)
	}

	// The original RECURRENCE-ID still deletes the override.
	if _, _, err := runChroncalCommand(t,
		"event", "delete", master.UID, "--recurrence-id", orig, "--yes"); err != nil {
		t.Fatalf("delete at original slot: %v", err)
	}

	a := openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Events.GetByUID(t.Context(), master.UID)
	if err != nil {
		t.Fatalf("master vanished: %v", err)
	}
	for _, ex := range strings.Split(got.ExDates, ",") {
		if strings.TrimSpace(ex) == "2026-09-02T13:00:00Z" {
			t.Fatal("phantom EXDATE written for the moved time")
		}
	}
}
