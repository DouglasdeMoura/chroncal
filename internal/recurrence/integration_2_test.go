package recurrence

import (
	"context"

	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func TestDeleteOverrideThenReexpand(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Create weekly event: 4 occurrences starting Apr 6.
	base := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Create override for Apr 13 instance.
	_, err = eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Weekly Sync (moved)",
		StartTime:    time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 13, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-13T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Verify expansion includes the override (4 instances).
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	before, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("expand before delete: %v", err)
	}
	if len(before) != 4 {
		t.Fatalf("before delete: got %d events, want 4", len(before))
	}

	// Delete the override (should add EXDATE to master).
	override, err := eventsSvc.GetByUIDAndRecurrenceID(ctx, master.UID, "2026-04-13T09:00:00Z")
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := eventsSvc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("delete override: %v", err)
	}

	// Re-expand: should now have 3 instances (Apr 13 excluded by EXDATE).
	after, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("expand after delete: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("after delete: got %d events, want 3", len(after))
	}

	// Verify Apr 13 is not in the results.
	excluded := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)
	for _, e := range after {
		if e.StartTime.Equal(excluded) {
			t.Error("Apr 13 instance should be excluded after override deletion")
		}
	}
}

// TestMovedOverride_OutOfSlotWindow verifies that a moved occurrence appears on
// its new day. It is absent from the day of the slot it replaced. That holds
// across every event expansion path (ListExpandedByDateRange, ListExpandedEvents,
// ListFilteredEvents). Regression test for the moved-override-day bug.
func TestMovedOverride_OutOfSlotWindow(t *testing.T) {
	ctx := context.Background()
	_, recurSvc := movedOverrideFixture(t)

	// Window covering only the original slot day (Apr 6). The occurrence moved
	// away, so nothing should show here.
	slotFrom := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)
	slotTo := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)

	// Window covering only the new day (Apr 8). Exactly the moved override.
	movedFrom := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	movedTo := time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)

	wantMoved := time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC)

	t.Run("ListExpandedByDateRange", func(t *testing.T) {
		slot, err := recurSvc.ListExpandedByDateRange(ctx, slotFrom, slotTo)
		if err != nil {
			t.Fatalf("expand slot window: %v", err)
		}
		if len(slot) != 0 {
			t.Errorf("original slot window: got %d events, want 0", len(slot))
		}
		moved, err := recurSvc.ListExpandedByDateRange(ctx, movedFrom, movedTo)
		if err != nil {
			t.Fatalf("expand moved window: %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("moved window: got %d events, want 1", len(moved))
		}
		if !moved[0].StartTime.Equal(wantMoved) {
			t.Errorf("moved start = %v, want %v", moved[0].StartTime, wantMoved)
		}
		if moved[0].Title != "Weekly Standup (moved)" {
			t.Errorf("moved title = %q, want %q", moved[0].Title, "Weekly Standup (moved)")
		}
	})

	t.Run("ListExpandedEvents", func(t *testing.T) {
		slot, err := recurSvc.ListExpandedEvents(ctx, slotFrom, slotTo)
		if err != nil {
			t.Fatalf("expand slot window: %v", err)
		}
		if len(slot) != 0 {
			t.Errorf("original slot window: got %d events, want 0", len(slot))
		}
		moved, err := recurSvc.ListExpandedEvents(ctx, movedFrom, movedTo)
		if err != nil {
			t.Fatalf("expand moved window: %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("moved window: got %d events, want 1", len(moved))
		}
		if !moved[0].StartTime.Equal(wantMoved) {
			t.Errorf("moved start = %v, want %v", moved[0].StartTime, wantMoved)
		}
	})

	t.Run("ListFilteredEvents", func(t *testing.T) {
		slot, err := recurSvc.ListFilteredEvents(ctx, EventListParams{From: slotFrom, To: slotTo})
		if err != nil {
			t.Fatalf("filter slot window: %v", err)
		}
		if len(slot) != 0 {
			t.Errorf("original slot window: got %d events, want 0", len(slot))
		}
		moved, err := recurSvc.ListFilteredEvents(ctx, EventListParams{From: movedFrom, To: movedTo})
		if err != nil {
			t.Fatalf("filter moved window: %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("moved window: got %d events, want 1", len(moved))
		}
		if !moved[0].StartTime.Equal(wantMoved) {
			t.Errorf("moved start = %v, want %v", moved[0].StartTime, wantMoved)
		}
	})
}

// TestMovedOverride_ExDatedSlot verifies that an override whose RECURRENCE-ID
// slot is also EXDATE'd on the master is still emitted. It is not dropped as an
// orphan. RFC 5545 lets a master carry an EXDATE and a separate RECURRENCE-ID
// override for the same slot. The override replaces (wins over) the slot.
// Orphan detection must then ignore EXDATEs when it checks whether the slot is
// a genuine master occurrence.
func TestMovedOverride_ExDatedSlot(t *testing.T) {
	ctx := context.Background()
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)

	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Standup",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC), // Monday
		EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		ExDates:        "2026-04-13T09:00:00Z", // the slot the override replaces
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	// Override the EXDATE'd Apr 13 slot, moved to Apr 14 14:00.
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Standup (moved)",
		StartTime:    time.Date(2026, 4, 14, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 14, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-13T09:00:00Z",
	}); err != nil {
		t.Fatalf("create override: %v", err)
	}

	from := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, 4, 14, 14, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (override must survive an EXDATE'd slot)", len(events))
	}
	if !events[0].StartTime.Equal(want) {
		t.Errorf("start = %v, want %v", events[0].StartTime, want)
	}
}

// TestMovedOverride_MultiDaySpansWindow verifies that a moved override which is
// a multi-day event appears in a queried window it overlaps. That holds even
// when its start precedes the window. Override emission must use [start, end)
// overlap. That
// matches the non-recurring range path, not start-in-window. Otherwise a
// multi-day override is dropped from every day after its start.
func TestMovedOverride_MultiDaySpansWindow(t *testing.T) {
	ctx := context.Background()
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)

	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Standup",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC), // Monday
		EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	// Move the Apr 6 occurrence to a multi-day span: Apr 5 10:00 -> Apr 8 12:00.
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Standup Offsite (moved)",
		StartTime:    time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-06T09:00:00Z",
	}); err != nil {
		t.Fatalf("create multi-day override: %v", err)
	}

	// Query a single day (Apr 7) the override spans but does not start on, and on
	// which the master produces no instance (Apr 7 is not a Monday).
	from := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	wantStart := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name  string
		start func() (time.Time, int, error)
	}{
		{"ListExpandedByDateRange", func() (time.Time, int, error) {
			e, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
			if len(e) == 0 {
				return time.Time{}, len(e), err
			}
			return e[0].StartTime, len(e), err
		}},
		{"ListExpandedEvents", func() (time.Time, int, error) {
			e, err := recurSvc.ListExpandedEvents(ctx, from, to)
			if len(e) == 0 {
				return time.Time{}, len(e), err
			}
			return e[0].StartTime, len(e), err
		}},
		{"ListFilteredEvents", func() (time.Time, int, error) {
			e, err := recurSvc.ListFilteredEvents(ctx, EventListParams{From: from, To: to})
			if len(e) == 0 {
				return time.Time{}, len(e), err
			}
			return e[0].StartTime, len(e), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, n, err := tc.start()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if n != 1 {
				t.Fatalf("got %d events, want 1 (multi-day override overlapping the window)", n)
			}
			if !start.Equal(wantStart) {
				t.Errorf("start = %v, want %v", start, wantStart)
			}
		})
	}
}

// TestMovedOverride_OrphanDropped verifies that an override whose RECURRENCE-ID
// is not a genuine occurrence of its master (e.g. left behind after the series
// was truncated or split) is not expanded, even when its own start falls inside
// the query window. This is the shape that produced a phantom occurrence when a
// recurring series was rescheduled "this and following".
func TestMovedOverride_OrphanDropped(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Master generates only Apr 6 and Apr 13 (COUNT=2).
	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Truncated Weekly",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC), // Monday
		EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO;COUNT=2",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Orphan override: RECURRENCE-ID Apr 20 is past the COUNT=2 series, so the
	// master never produces that occurrence.
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Orphan",
		StartTime:    time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-20T09:00:00Z",
	}); err != nil {
		t.Fatalf("create orphan override: %v", err)
	}

	from := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		run  func() (int, error)
	}{
		{"ListExpandedByDateRange", func() (int, error) {
			e, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
			return len(e), err
		}},
		{"ListExpandedEvents", func() (int, error) {
			e, err := recurSvc.ListExpandedEvents(ctx, from, to)
			return len(e), err
		}},
		{"ListFilteredEvents", func() (int, error) {
			e, err := recurSvc.ListFilteredEvents(ctx, EventListParams{From: from, To: to})
			return len(e), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := tc.run()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if n != 0 {
				t.Errorf("orphan override expanded: got %d events, want 0", n)
			}
		})
	}
}

// TestAllDayDateOnlyOverride_Consistent guards the suppression/occursAt
// agreement. An all-day master with an override that carries a date-only
// RECURRENCE-ID must expand to exactly one occurrence per day. Never a
// duplicate (slot shown plus override). Never a vanished day (slot suppressed
// and override dropped). Both checks normalize the recurrence_id the same way
// (canonicalRecurrenceID). They always agree on count. Which row wins for a
// date-only id is host-timezone dependent (all-day rows are stored at
// local-midnight). It does not matter here. Sync and import always emit
// full UTC RFC 3339 recurrence_ids. The date-only form is then a defensive edge.
func TestAllDayDateOnlyOverride_Consistent(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: "allday-daily", CalendarID: 1, Title: "Daily AllDay",
		StartTime:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		AllDay:         true,
		RecurrenceRule: "FREQ=DAILY",
	}); err != nil {
		t.Fatalf("create all-day master: %v", err)
	}
	// In-place edit of the Apr 8 occurrence with a date-only RECURRENCE-ID.
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: "allday-daily", CalendarID: 1, Title: "Daily AllDay (edited)",
		StartTime:    time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC),
		AllDay:       true,
		RecurrenceID: "2026-04-08",
	}); err != nil {
		t.Fatalf("create all-day override: %v", err)
	}

	from := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		run  func() (int, error)
	}{
		{"ListExpandedByDateRange", func() (int, error) {
			e, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
			return len(e), err
		}},
		{"ListExpandedEvents", func() (int, error) {
			e, err := recurSvc.ListExpandedEvents(ctx, from, to)
			return len(e), err
		}},
		{"ListFilteredEvents", func() (int, error) {
			e, err := recurSvc.ListFilteredEvents(ctx, EventListParams{From: from, To: to})
			return len(e), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := tc.run()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if n != 1 {
				t.Errorf("all-day Apr 8: got %d events, want exactly 1 (no duplicate, no vanish)", n)
			}
		})
	}
}

// TestMovedOverride_Todo verifies the same moved-occurrence semantics for the
// recurring todo expansion path.
func TestMovedOverride_Todo(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	master, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Weekly Review",
		DueDate:        "2026-04-06", // Monday
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring todo: %v", err)
	}
	// Move the Apr 6 occurrence to Wednesday Apr 8.
	if _, err := todoSvc.UpsertByUID(ctx, todo.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Summary:      "Weekly Review (moved)",
		DueDate:      "2026-04-08",
		RecurrenceID: "2026-04-06T00:00:00Z",
	}); err != nil {
		t.Fatalf("create todo override: %v", err)
	}

	// Original slot day: nothing (occurrence moved away).
	slot, err := recurSvc.ListExpandedTodosByDueDateRange(ctx,
		time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("expand slot window: %v", err)
	}
	if len(slot) != 0 {
		for i, td := range slot {
			t.Logf("  slot[%d]: %s due=%s", i, td.Summary, td.DueDate)
		}
		t.Errorf("original slot window: got %d todos, want 0", len(slot))
	}

	// New day: exactly the moved override.
	moved, err := recurSvc.ListExpandedTodosByDueDateRange(ctx,
		time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("expand moved window: %v", err)
	}
	if len(moved) != 1 {
		t.Fatalf("moved window: got %d todos, want 1", len(moved))
	}
	if moved[0].DueDate != "2026-04-08" {
		t.Errorf("moved due = %q, want %q", moved[0].DueDate, "2026-04-08")
	}
}

// TestMovedOverride_TodoMultiDaySpansWindow verifies that a multi-day todo
// override whose START precedes the query window but whose DUE falls inside it
// is kept. The override window check must use [START, DUE) interval overlap
// (it honors todoDuration). That matches the master-occurrence path and the
// event override path. It is not a point test on the anchor alone. That would
// drop the override from every window after its start. Regression test for
// issue #288.
func TestMovedOverride_TodoMultiDaySpansWindow(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	master, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Weekly Review",
		StartDate:      "2026-04-06T09:00:00Z", // Monday
		DueDate:        "2026-04-06T10:00:00Z",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring todo: %v", err)
	}
	// Move the Apr 6 occurrence to a multi-day span: START Apr 5 10:00 ->
	// DUE Apr 8 12:00.
	if _, err := todoSvc.UpsertByUID(ctx, todo.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Summary:      "Review Offsite (moved)",
		StartDate:    "2026-04-05T10:00:00Z",
		DueDate:      "2026-04-08T12:00:00Z",
		RecurrenceID: "2026-04-06T09:00:00Z",
	}); err != nil {
		t.Fatalf("create multi-day todo override: %v", err)
	}

	// Query a single day (Apr 7) the override spans but does not start on, and
	// on which the master produces no instance (Apr 7 is not a Monday).
	from := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)

	got, err := recurSvc.ListExpandedTodosByDueDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("expand spanning window: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d todos, want 1 (multi-day override overlapping the window)", len(got))
	}
	if got[0].StartDate != "2026-04-05T10:00:00Z" {
		t.Errorf("start = %q, want %q", got[0].StartDate, "2026-04-05T10:00:00Z")
	}
}

// TestListExpandedByDateRange_OverrideEmptyEndTime locks in that an override
// persisted with a blank/zero end_time (e.g. a point-in-time or improperly
// migrated override) still appears. Previously overlapsWindow required
// end.After(from). A zero EndTime parsed from an empty string was then treated
// as no overlap. The override was dropped in silence. The master slot it
// replaces is suppressed. The occurrence then vanished entirely.
// Regression test for issue #127.
func TestListExpandedByDateRange_OverrideEmptyEndTime(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Weekly event: 4 occurrences starting Apr 6.
	base := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Override the Apr 13 instance in place (same start as the slot).
	override, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Sync (overridden)",
		StartTime:    time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-13T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Simulate a row persisted with a blank end_time.
	if _, err := db.ExecContext(ctx,
		"UPDATE events SET end_time = '' WHERE id = ?", override.ID); err != nil {
		t.Fatalf("blank end_time: %v", err)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// 4 occurrences: Apr 6, Apr 13 (overridden), Apr 20, Apr 27.
	if len(events) != 4 {
		for i, e := range events {
			t.Logf("  events[%d]: %s at %v", i, e.Title, e.StartTime)
		}
		t.Fatalf("got %d events, want 4", len(events))
	}

	// The Apr 13 occurrence must be present and be the override.
	want := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)
	var found *event.Event
	for i := range events {
		if events[i].StartTime.Equal(want) {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("Apr 13 occurrence missing from expansion")
	}
	if found.Title != "Weekly Sync (overridden)" {
		t.Errorf("Apr 13 title = %q, want %q", found.Title, "Weekly Sync (overridden)")
	}
}

// TestMovedOverride_Journal is the journal analogue of TestMovedOverride_Todo.
// It exercises the recurring-journal merge path end to end. That is
// overridden-slot suppression and override emission at the moved occurrence.
func TestMovedOverride_Journal(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	journalSvc := journal.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	master, err := journalSvc.Create(ctx, journal.CreateParams{
		CalendarID:     1,
		Summary:        "Weekly Reflection",
		StartDate:      "2026-04-06", // Monday
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring journal: %v", err)
	}
	// Move the Apr 6 occurrence to Wednesday Apr 8.
	if _, err := journalSvc.UpsertByUID(ctx, journal.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Summary:      "Weekly Reflection (moved)",
		StartDate:    "2026-04-08",
		RecurrenceID: "2026-04-06T00:00:00Z",
	}); err != nil {
		t.Fatalf("create journal override: %v", err)
	}

	// Original slot day: nothing (occurrence moved away).
	slot, err := recurSvc.ListFilteredJournals(ctx, JournalListParams{
		From: time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("expand slot window: %v", err)
	}
	if len(slot) != 0 {
		for i, j := range slot {
			t.Logf("  slot[%d]: %s start=%s", i, j.Summary, j.StartDate)
		}
		t.Errorf("original slot window: got %d journals, want 0", len(slot))
	}

	// New day: exactly the moved override.
	moved, err := recurSvc.ListFilteredJournals(ctx, JournalListParams{
		From: time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("expand moved window: %v", err)
	}
	if len(moved) != 1 {
		t.Fatalf("moved window: got %d journals, want 1", len(moved))
	}
	if moved[0].StartDate != "2026-04-08" {
		t.Errorf("moved start = %q, want %q", moved[0].StartDate, "2026-04-08")
	}
	if moved[0].Summary != "Weekly Reflection (moved)" {
		t.Errorf("moved summary = %q, want %q", moved[0].Summary, "Weekly Reflection (moved)")
	}
}

// TestListFilteredJournals_RecurringExpansion locks in that a recurring journal
// master expands into per-occurrence instances within the window, with
// StartDate adjusted to each occurrence.
func TestListFilteredJournals_RecurringExpansion(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	journalSvc := journal.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	if _, err := journalSvc.Create(ctx, journal.CreateParams{
		CalendarID:     1,
		Summary:        "Daily Log",
		StartDate:      "2026-04-01",
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	}); err != nil {
		t.Fatalf("create recurring journal: %v", err)
	}

	journals, err := recurSvc.ListFilteredJournals(ctx, JournalListParams{
		From: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListFilteredJournals: %v", err)
	}
	// Apr 1..5 inclusive (Apr 6 == to excluded).
	if len(journals) != 5 {
		for i, j := range journals {
			t.Logf("  journals[%d]: %s start=%s", i, j.Summary, j.StartDate)
		}
		t.Fatalf("got %d journals, want 5", len(journals))
	}
	for i, j := range journals {
		want := time.Date(2026, 4, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		if j.StartDate != want {
			t.Errorf("journals[%d].StartDate = %q, want %q", i, j.StartDate, want)
		}
	}
}

// TestExpand_OverrideFetchErrorPropagates locks in that a failure on a fetch of
// a recurring master's overrides surfaces as an error. It does not degrade to
// "no overrides" in silence. That would suppress nothing. It would emit the
// stale master RRULE instance at its original time. The real override vanishes.
// Regression test for issue #251.
func TestExpand_OverrideFetchErrorPropagates(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	ctx := context.Background()

	// Weekly-Monday master whose Apr 6 occurrence is moved to Wed Apr 8 14:00.
	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Standup",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC), // Monday
		EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Standup (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-06T09:00:00Z",
	}); err != nil {
		t.Fatalf("create override: %v", err)
	}

	// A recurrence service whose override fetch fails. The master itself still
	// loads, so expansion reaches the override fetch.
	faultySvc := NewService(db, storage.New(faultyDBTX{DBTX: db, failOn: "ListOverridesByUID"}))

	// Original slot day (Apr 6): with overrides discarded the master would emit
	// the stale Apr 6 instance here; the fix must surface the error instead.
	from := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		run  func() (int, error)
	}{
		{"ListExpandedEvents", func() (int, error) {
			e, err := faultySvc.ListExpandedEvents(ctx, from, to)
			return len(e), err
		}},
		{"ListExpandedByDateRange", func() (int, error) {
			e, err := faultySvc.ListExpandedByDateRange(ctx, from, to)
			return len(e), err
		}},
		{"ListFilteredEvents", func() (int, error) {
			e, err := faultySvc.ListFilteredEvents(ctx, EventListParams{From: from, To: to})
			return len(e), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := tc.run()
			if err == nil {
				t.Fatalf("override fetch failed but %s returned nil error with %d events "+
					"(stale master instance leaked instead of surfacing the error)", tc.name, n)
			}
		})
	}
}

// TestExpandedInstancesCarryConferenceURI guards against the recurrence mapper
// that drifts from event.FromStorage. A recurring event with a ConferenceURI
// must keep that URI on every expanded instance (regression test for #256).
func TestExpandedInstancesCarryConferenceURI(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	const confURI = "https://meet.example.com/weekly-room"
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO;COUNT=3",
		ConferenceURI:  confURI,
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i := range events {
		if events[i].ConferenceURI != confURI {
			t.Errorf("events[%d].ConferenceURI = %q, want %q", i, events[i].ConferenceURI, confURI)
		}
	}
}

// TestListExpandedByDateRange_RDateOnly verifies the full DB-level path for
// RDATE-only recurrence (no RRULE). Previously the ListRecurringEvents SQL
// query excluded such rows (recurrence_rule IS NOT NULL). They were then
// emitted as non-recurring singletons in silence. Only their DTSTART
// occurrence was visible (issue #362).
func TestListExpandedByDateRange_RDateOnly(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)

	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "RDATE-only event",
		StartTime:  base,
		EndTime:    base.Add(time.Hour),
		// No RecurrenceRule: pure RDATE recurrence.
		RDates: rdate1.Format(time.RFC3339) + "," + rdate2.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Expect: DTSTART (Apr 1) + RDATE1 (Apr 15) + RDATE2 (Apr 22) = 3 instances.
	if len(events) != 3 {
		for i, e := range events {
			t.Logf("  events[%d]: %s start=%v", i, e.Title, e.StartTime)
		}
		t.Fatalf("got %d events, want 3 (DTSTART + 2 RDATEs); RDATE-only expansion broken", len(events))
	}

	wantTimes := []time.Time{base, rdate1, rdate2}
	for i, want := range wantTimes {
		if !events[i].StartTime.Equal(want) {
			t.Errorf("events[%d].StartTime = %v, want %v", i, events[i].StartTime, want)
		}
		if events[i].Title != "RDATE-only event" {
			t.Errorf("events[%d].Title = %q, want %q", i, events[i].Title, "RDATE-only event")
		}
	}
}

// TestListExpandedEvents_RDateOnly verifies the TUI display path
// (ListExpandedEvents) for RDATE-only recurrence.
func TestListExpandedEvents_RDateOnly(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)

	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "RDATE-only display",
		StartTime:  base,
		EndTime:    base.Add(time.Hour),
		RDates:     rdate1.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	expanded, err := recurSvc.ListExpandedEvents(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedEvents: %v", err)
	}

	if len(expanded) != 2 {
		for i, e := range expanded {
			t.Logf("  expanded[%d]: %s instanceTime=%v", i, e.Title, e.InstanceTime)
		}
		t.Fatalf("got %d expanded events, want 2 (DTSTART + 1 RDATE)", len(expanded))
	}

	wantTimes := []time.Time{base, rdate1}
	for i, want := range wantTimes {
		if !expanded[i].InstanceTime.Equal(want) {
			t.Errorf("expanded[%d].InstanceTime = %v, want %v", i, expanded[i].InstanceTime, want)
		}
	}
}
