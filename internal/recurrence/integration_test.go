package recurrence

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// faultyDBTX wraps a storage.DBTX and forces any query whose text contains
// failOn to fail, simulating a transient SQLite error mid-expansion. sqlc keeps
// the `-- name: <QueryName>` comment in the query string, so matching on the
// query name targets exactly one statement.
type faultyDBTX struct {
	storage.DBTX
	failOn string
}

func (f faultyDBTX) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if strings.Contains(query, f.failOn) {
		return nil, errors.New("injected query failure")
	}
	return f.DBTX.QueryContext(ctx, query, args...)
}

// TestExpand_OverrideFetchErrorPropagates locks in that a failure fetching a
// recurring master's overrides surfaces as an error instead of silently
// degrading to "no overrides" — which would suppress nothing and emit the stale
// master RRULE instance at its original time while the real override vanishes.
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

func TestListExpandedByDateRange(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring event with DTSTART far in the past (2020).
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Monday",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}

	// Non-recurring event inside query range.
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "One-off Meeting",
		StartTime:  time.Date(2026, 3, 31, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 3, 31, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	// Non-recurring event outside range — should not appear.
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Old Event",
		StartTime:  time.Date(2020, 6, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2020, 6, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Expect: Mon Mar 30, Tue Mar 31 (one-off), Mon Apr 6 = 3 events.
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	// Verify sorted by StartTime.
	if !sort.SliceIsSorted(events, func(i, j int) bool {
		return events[i].StartTime.Before(events[j].StartTime)
	}) {
		t.Error("events not sorted by StartTime")
	}

	// First should be the Monday Mar 30 instance.
	if events[0].Title != "Weekly Monday" {
		t.Errorf("events[0].Title = %q, want %q", events[0].Title, "Weekly Monday")
	}
	wantMar30 := time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC)
	if !events[0].StartTime.Equal(wantMar30) {
		t.Errorf("events[0].StartTime = %v, want %v", events[0].StartTime, wantMar30)
	}

	// Second should be the one-off on Mar 31.
	if events[1].Title != "One-off Meeting" {
		t.Errorf("events[1].Title = %q, want %q", events[1].Title, "One-off Meeting")
	}

	// Third should be the Monday Apr 6 instance.
	wantApr6 := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	if !events[2].StartTime.Equal(wantApr6) {
		t.Errorf("events[2].StartTime = %v, want %v", events[2].StartTime, wantApr6)
	}

	// EndTime should be adjusted (1 hour duration preserved).
	wantEnd := wantMar30.Add(time.Hour)
	if !events[0].EndTime.Equal(wantEnd) {
		t.Errorf("events[0].EndTime = %v, want %v", events[0].EndTime, wantEnd)
	}
}

func TestListExpandedByDateRange_MultiDayOverlap(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Multi-day event: starts March 28, ends April 2.
	// Query window [March 30, April 5) — event overlaps but starts before window.
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Multi-Day Conference",
		StartTime:  time.Date(2026, 3, 28, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 2, 17, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create multi-day: %v", err)
	}

	// Single-day event inside the range (control).
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Normal Meeting",
		StartTime:  time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create normal: %v", err)
	}

	// Event entirely before the range — should NOT appear.
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Past Event",
		StartTime:  time.Date(2026, 3, 25, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 3, 26, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create past: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	if len(events) != 2 {
		for i, e := range events {
			t.Logf("  events[%d]: %s start=%v end=%v", i, e.Title, e.StartTime, e.EndTime)
		}
		t.Fatalf("got %d events, want 2", len(events))
	}

	titles := map[string]bool{}
	for _, e := range events {
		titles[e.Title] = true
	}
	if !titles["Multi-Day Conference"] {
		t.Error("multi-day event not found in results")
	}
	if !titles["Normal Meeting"] {
		t.Error("normal meeting not found in results")
	}
}

func TestListExpandedByDateRange_ExDate(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	excluded := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC) // second Monday
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Except One",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		ExDates:        excluded.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Only 1 Monday (Mar 30) — Apr 6 is excluded.
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	for _, e := range events {
		if e.StartTime.Equal(excluded) {
			t.Error("excluded date appeared in results")
		}
	}
}

func TestListExpandedByDateRange_NoDuplication(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring event whose DTSTART is inside the query range.
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Daily For 3",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Should get exactly 3 instances, not duplicated.
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
}

// TestExportExpandedByDateRange_IncludesCancelledMaster guards that the
// display-time cancelled-master suppression does NOT leak into ICS export: a
// CANCELLED recurring master starting before the export window must still be
// emitted, since STATUS:CANCELLED is how a downstream client is told to drop
// the series.
// TestCancelledMaster_DropsLiveOverride locks in Google/iCloud whole-series
// cancel parity: when a recurring master is CANCELLED, even a still-CONFIRMED
// override instance is suppressed from display/alarms/free-busy (all of which
// flow through expansion). This is a deliberate behavior, not an accident — a
// surviving instance of a cancelled series must not linger.
func TestCancelledMaster_DropsLiveOverride(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC), // Monday
		EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	// Cancel the whole series.
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Weekly",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		Status:         "CANCELLED",
	}); err != nil {
		t.Fatalf("cancel master: %v", err)
	}
	// A still-CONFIRMED override on the Apr 13 instance, moved to 14:00.
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Weekly (kept instance)",
		StartTime:    time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 13, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-13T09:00:00Z",
		Status:       "CONFIRMED",
	}); err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Wide window covering several would-be occurrences and the override.
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}
	if len(events) != 0 {
		for i, e := range events {
			t.Logf("  events[%d]: %s at %v (status=%s)", i, e.Title, e.StartTime, e.Status)
		}
		t.Fatalf("cancelled series produced %d events, want 0 (whole-series cancel)", len(events))
	}
}

func TestExportExpandedByDateRange_IncludesCancelledMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Cancelled Weekly Export",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC), // before the window
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Cancelled Weekly Export",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		Status:         "CANCELLED",
	}); err != nil {
		t.Fatalf("cancel master: %v", err)
	}

	events, err := recurSvc.ExportExpandedByDateRange(ctx, ExportFilterParams{
		From: time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ExportExpandedByDateRange: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (cancelled master must still export)", len(events))
	}
	if !strings.EqualFold(events[0].Status, "CANCELLED") {
		t.Errorf("exported master Status = %q, want CANCELLED", events[0].Status)
	}
	if events[0].RecurrenceRule == "" {
		t.Error("exported master lost its RecurrenceRule")
	}
}

func TestExportExpandedByDateRange(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring event from the past.
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Export",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Non-recurring event in range.
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "One-off Export",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ExportExpandedByDateRange(ctx, ExportFilterParams{
		From: from,
		To:   to,
	})
	if err != nil {
		t.Fatalf("ExportExpandedByDateRange: %v", err)
	}

	// Should return 2 master events (not expanded instances).
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	// The recurring event should retain its original DTSTART (master, not instance).
	for _, e := range events {
		if e.Title == "Weekly Export" {
			if e.StartTime.Year() != 2020 {
				t.Errorf("export master StartTime.Year = %d, want 2020", e.StartTime.Year())
			}
			if e.RecurrenceRule == "" {
				t.Error("export master lost its RecurrenceRule")
			}
		}
	}
}

func TestListExpandedByDateRange_OverrideMerging(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Weekly Monday event.
	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Standup",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Override: move Apr 6 instance to Thursday Apr 9 at 14:00.
	_, err = eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Standup (moved)",
		StartTime:    time.Date(2026, 4, 9, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 9, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-06T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Expect: Mar 30 (original), Apr 9 (override), NOT Apr 6 (replaced).
	if len(events) != 2 {
		for i, e := range events {
			t.Logf("  events[%d]: %s at %v", i, e.Title, e.StartTime)
		}
		t.Fatalf("got %d events, want 2", len(events))
	}

	// First: Mar 30 original instance.
	if events[0].Title != "Weekly Standup" {
		t.Errorf("events[0].Title = %q, want %q", events[0].Title, "Weekly Standup")
	}
	wantMar30 := time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC)
	if !events[0].StartTime.Equal(wantMar30) {
		t.Errorf("events[0].StartTime = %v, want %v", events[0].StartTime, wantMar30)
	}

	// Second: Apr 9 override (not Apr 6).
	if events[1].Title != "Weekly Standup (moved)" {
		t.Errorf("events[1].Title = %q, want %q", events[1].Title, "Weekly Standup (moved)")
	}
	wantApr9 := time.Date(2026, 4, 9, 14, 0, 0, 0, time.UTC)
	if !events[1].StartTime.Equal(wantApr9) {
		t.Errorf("events[1].StartTime = %v, want %v", events[1].StartTime, wantApr9)
	}
}

func TestListExpandedByDateRange_CancelledOverride(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Daily",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Cancel the Apr 2 instance.
	_, err = eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Daily",
		StartTime:    time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-02T09:00:00Z",
		Status:       "CANCELLED",
	})
	if err != nil {
		t.Fatalf("create cancelled override: %v", err)
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	// Should get 2 instances (Apr 1, Apr 3). Apr 2 is cancelled.
	if len(events) != 2 {
		for i, e := range events {
			t.Logf("  events[%d]: %s at %v (status=%s)", i, e.Title, e.StartTime, e.Status)
		}
		t.Fatalf("got %d events, want 2", len(events))
	}

	for _, e := range events {
		if e.StartTime.Day() == 2 {
			t.Error("cancelled Apr 2 instance appeared in results")
		}
	}
}

func TestListFilteredEvents_DefaultIncludesRecurring(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring weekly event.
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      time.Date(2020, 1, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}

	// Non-recurring event.
	_, err = eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "One-off Meeting",
		StartTime:  time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	// Default list with no date range must include recurring masters,
	// mirroring the todo/journal contract.
	events, err := recurSvc.ListFilteredEvents(ctx, EventListParams{})
	if err != nil {
		t.Fatalf("ListFilteredEvents: %v", err)
	}

	if len(events) != 2 {
		for i, e := range events {
			t.Logf("  events[%d]: %s start=%s rrule=%s", i, e.Title, e.StartTime, e.RecurrenceRule)
		}
		t.Fatalf("got %d events, want 2", len(events))
	}

	found := map[string]bool{}
	for _, e := range events {
		found[e.Title] = true
	}
	if !found["Weekly Sync"] {
		t.Error("missing recurring event 'Weekly Sync'")
	}
	if !found["One-off Meeting"] {
		t.Error("missing one-off event 'One-off Meeting'")
	}
}

func TestListFilteredTodos_DefaultIncludesRecurring(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring weekly todo.
	_, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Weekly Review",
		DueDate:        "2020-01-06",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}

	// Non-recurring todo.
	_, err = todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "One-off Task",
		DueDate:    "2026-04-01",
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	// Default list with no date range must include recurring masters.
	todos, err := recurSvc.ListFilteredTodos(ctx, TodoListParams{})
	if err != nil {
		t.Fatalf("ListFilteredTodos: %v", err)
	}

	if len(todos) != 2 {
		for i, td := range todos {
			t.Logf("  todos[%d]: %s due=%s rrule=%s", i, td.Summary, td.DueDate, td.RecurrenceRule)
		}
		t.Fatalf("got %d todos, want 2", len(todos))
	}

	// Verify both are present.
	found := map[string]bool{}
	for _, td := range todos {
		found[td.Summary] = true
	}
	if !found["Weekly Review"] {
		t.Error("missing recurring todo 'Weekly Review'")
	}
	if !found["One-off Task"] {
		t.Error("missing one-off todo 'One-off Task'")
	}
}

func TestListFilteredTodos_FiltersApplyToRecurring(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring todo with NEEDS-ACTION status.
	_, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Active Recurring",
		DueDate:        "2020-01-06",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Recurring todo that is completed.
	completed, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Done Recurring",
		DueDate:        "2020-01-06",
		RecurrenceRule: "FREQ=DAILY",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	todoSvc.Complete(ctx, completed.ID)

	// Filter by status NEEDS-ACTION — only active recurring should appear.
	todos, err := recurSvc.ListFilteredTodos(ctx, TodoListParams{Status: "NEEDS-ACTION"})
	if err != nil {
		t.Fatalf("ListFilteredTodos: %v", err)
	}

	if len(todos) != 1 {
		for i, td := range todos {
			t.Logf("  todos[%d]: %s status=%s", i, td.Summary, td.Status)
		}
		t.Fatalf("got %d todos, want 1", len(todos))
	}
	if todos[0].Summary != "Active Recurring" {
		t.Errorf("Summary = %q, want %q", todos[0].Summary, "Active Recurring")
	}
}

func TestListExpandedTodosByDueDateRange(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Recurring weekly todo with DUE in the past.
	_, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID:     1,
		Summary:        "Weekly Review",
		DueDate:        "2020-01-06",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}

	// Non-recurring todo inside range.
	_, err = todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "One-off Task",
		DueDate:    "2026-04-01",
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	todos, err := recurSvc.ListExpandedTodosByDueDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedTodosByDueDateRange: %v", err)
	}

	// Expect: Mar 30 (recurring), Apr 1 (one-off), Apr 6 (recurring) = 3.
	// Apr 13 is excluded by half-open [from, to) semantics.
	if len(todos) != 3 {
		for i, td := range todos {
			t.Logf("  todos[%d]: %s due=%s", i, td.Summary, td.DueDate)
		}
		t.Fatalf("got %d todos, want 3", len(todos))
	}

	// First should be the Monday Mar 30 instance.
	if todos[0].Summary != "Weekly Review" {
		t.Errorf("todos[0].Summary = %q, want %q", todos[0].Summary, "Weekly Review")
	}
	if todos[0].DueDate != "2026-03-30" {
		t.Errorf("todos[0].DueDate = %q, want %q", todos[0].DueDate, "2026-03-30")
	}

	// Second should be the one-off on Apr 1.
	if todos[1].Summary != "One-off Task" {
		t.Errorf("todos[1].Summary = %q, want %q", todos[1].Summary, "One-off Task")
	}
}

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

// movedOverrideFixture creates a weekly-Monday master and moves its Apr 6
// (Monday) occurrence to Apr 8 (Wednesday) at 14:00. The override's RECURRENCE-ID
// slot (Apr 6) and its new start (Apr 8) fall on different days, which is the
// shape that previously made a moved occurrence surface on the wrong day.
func movedOverrideFixture(t *testing.T) (*event.Service, *Service) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

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
	// Move the Apr 6 occurrence to Wednesday Apr 8.
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Standup (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC), // Wednesday
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-06T09:00:00Z",
	}); err != nil {
		t.Fatalf("create override: %v", err)
	}
	return eventsSvc, recurSvc
}

// TestMovedOverride_OutOfSlotWindow verifies that a moved occurrence appears on
// its new day and is absent from the day of the slot it replaced, across every
// event expansion path (ListExpandedByDateRange, ListExpandedEvents,
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

// TestMovedOverride_WiderWindowNoDuplication verifies that a window spanning both
// the original slot and the moved day yields the moved occurrence once (not the
// suppressed Apr 6 slot, and not a duplicate), alongside the series' other
// untouched occurrences.
func TestMovedOverride_WiderWindowNoDuplication(t *testing.T) {
	ctx := context.Background()
	_, recurSvc := movedOverrideFixture(t)

	// Apr 6 through Apr 19 (exclusive): master Mondays are Apr 6 (moved away)
	// and Apr 13. Expect the moved override on Apr 8 and the Apr 13 instance.
	from := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)

	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(events) != 2 {
		for i, e := range events {
			t.Logf("  events[%d]: %s at %v", i, e.Title, e.StartTime)
		}
		t.Fatalf("got %d events, want 2", len(events))
	}
	wantApr8 := time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC)
	wantApr13 := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)
	if !events[0].StartTime.Equal(wantApr8) {
		t.Errorf("events[0].StartTime = %v, want %v", events[0].StartTime, wantApr8)
	}
	if !events[1].StartTime.Equal(wantApr13) {
		t.Errorf("events[1].StartTime = %v, want %v", events[1].StartTime, wantApr13)
	}
	for _, e := range events {
		if e.StartTime.Day() == 6 {
			t.Error("Apr 6 slot should be suppressed (its occurrence moved to Apr 8)")
		}
	}
}

// TestMovedOverride_ExDatedSlot verifies that an override whose RECURRENCE-ID
// slot is also EXDATE'd on the master is still emitted, not dropped as an
// orphan. RFC 5545 lets a master carry an EXDATE and a separate RECURRENCE-ID
// override for the same slot; the override replaces (wins over) the slot, so
// orphan detection must ignore EXDATEs when checking whether the slot is a
// genuine master occurrence.
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
// a multi-day event appears in a queried window it overlaps even when its start
// precedes the window. Override emission must use [start, end) overlap, matching
// the non-recurring range path, not start-in-window — otherwise a multi-day
// override is dropped from every day after its start.
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
// agreement: an all-day master with an override carrying a date-only
// RECURRENCE-ID must expand to exactly one occurrence per day — never a
// duplicate (slot shown plus override) nor a vanished day (slot suppressed and
// override dropped). Both checks normalize the recurrence_id the same way
// (canonicalRecurrenceID), so they always agree on count. Which row wins for a
// date-only id is host-timezone dependent (all-day rows are stored at
// local-midnight), but it does not matter here: sync and import always emit
// full UTC RFC 3339 recurrence_ids, so the date-only form is a defensive edge.
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

// TestListExpandedByDateRange_OverrideEmptyEndTime locks in that an override
// persisted with a blank/zero end_time (e.g. a point-in-time or improperly
// migrated override) still appears. Previously overlapsWindow required
// end.After(from), so a zero EndTime parsed from an empty string was treated as
// not overlapping and the override was silently dropped -- and because the
// master slot it replaces is suppressed, the occurrence vanished entirely.
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

// TestExpandedInstancesCarryConferenceURI guards against the recurrence mapper
// drifting from event.FromStorage: a recurring event with a ConferenceURI must
// keep that URI on every expanded instance (regression test for #256).
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
