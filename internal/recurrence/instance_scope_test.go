package recurrence

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return v
}

func weeklyMaster(t *testing.T) event.Event {
	return event.Event{
		UID:            "series@example.com",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,WE,FR",
		StartTime:      mustTime(t, "2026-09-02T10:00:00Z"),
		EndTime:        mustTime(t, "2026-09-02T11:00:00Z"),
		Status:         "CONFIRMED",
	}
}

// A moved override keeps its original RECURRENCE-ID as the deletion key. The
// raw set must still accept the original slot and reject the moved display
// start; otherwise a delete at the original slot errors (regression) or a
// delete at the new slot writes a phantom EXDATE (issue #745 via review).
func TestOccurrenceExistsAt_MovedOverrideKeepsOriginalSlot(t *testing.T) {
	master := weeklyMaster(t)
	orig := "2026-09-04T10:00:00Z" // Friday slot

	if !OccurrenceExistsAt(master, mustTime(t, orig)) {
		t.Fatal("original slot must exist before any override")
	}
	movedStart := mustTime(t, "2026-09-07T15:00:00Z")

	// The raw master is unchanged by an override existing elsewhere.
	if !OccurrenceExistsAt(master, mustTime(t, orig)) {
		t.Fatal("override must not hide its original slot")
	}
	if OccurrenceExistsAt(master, movedStart) {
		t.Fatal("moved display time must not count as an occurrence")
	}
}

// An EXDATE removes the slot for real: deleting it again is a no-op and the
// guard must say so instead of letting DeleteInstance append a duplicate.
func TestOccurrenceExistsAt_ExDateRemovesSlot(t *testing.T) {
	master := weeklyMaster(t)
	gone := mustTime(t, "2026-09-04T10:00:00Z")
	master.ExDates = gone.Format(time.RFC3339)
	if OccurrenceExistsAt(master, gone) {
		t.Fatal("EXDATEd slot must not exist")
	}
	if !OccurrenceExistsAt(master, mustTime(t, "2026-09-07T10:00:00Z")) {
		t.Fatal("other slots survive the EXDATE")
	}
}

func TestOccurrenceExistsAt_CancelledSeriesHasNothing(t *testing.T) {
	master := weeklyMaster(t)
	master.Status = "CANCELLED"
	if OccurrenceExistsAt(master, mustTime(t, "2026-09-02T10:00:00Z")) {
		t.Fatal("cancelled series must have no occurrences")
	}
	if HasOccurrenceFrom(master, time.Now()) {
		t.Fatal("cancelled series truncation must be rejected")
	}
	if _, ok := NextOccurrenceAfter(master, time.Now()); ok {
		t.Fatal("cancelled series must yield no next occurrence")
	}
}

// Non-recurring masters match only their own start.
func TestOccurrenceScope_NonRecurring(t *testing.T) {
	single := event.Event{StartTime: mustTime(t, "2026-09-02T10:00:00Z")}
	if !OccurrenceExistsAt(single, single.StartTime) {
		t.Fatal("own start must match")
	}
	if OccurrenceExistsAt(single, single.StartTime.Add(time.Hour)) {
		t.Fatal("non-recurring has one slot only")
	}
	if HasOccurrenceFrom(single, single.StartTime.Add(time.Minute)) {
		t.Fatal("nothing after the single instance")
	}
}

func TestHasOccurrenceFromAndNext(t *testing.T) {
	master := weeklyMaster(t)
	if !HasOccurrenceFrom(master, mustTime(t, "2026-09-04T10:00:00Z")) {
		t.Fatal("truncation at an existing slot removes it")
	}
	next, ok := NextOccurrenceAfter(master, mustTime(t, "2026-09-02T10:00:01Z"))
	if !ok || next.Format(time.RFC3339) != "2026-09-04T10:00:00Z" {
		t.Fatalf("next = %v ok=%v, want 2026-09-04T10:00:00Z", next, ok)
	}
	if !HasOccurrenceFrom(master, mustTime(t, "2031-01-01T00:00:00Z")) {
		t.Fatal("a weekly series still has occurrences five years out")
	}
}
