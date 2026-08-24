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
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// movedOverrideFixture creates a weekly-Monday master and moves its Apr 6
// (Monday) occurrence to Apr 8 (Wednesday) at 14:00. The override's RECURRENCE-ID
// slot (Apr 6) and its new start (Apr 8) fall on different days. That is the
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

// faultyDBTX wraps a storage.DBTX and forces any query whose text contains
// failOn to fail. That simulates a transient SQLite error mid-expansion. sqlc
// keeps the `-- name: <QueryName>` comment in the query string. A match on the
// query name then targets exactly one statement.
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
// display-time cancelled-master suppression does NOT leak into ICS export. A
// CANCELLED recurring master that starts before the export window must still
// be emitted. STATUS:CANCELLED is how a downstream client is told to drop
// the series.
// TestCancelledMaster_DropsLiveOverride locks in Google/iCloud whole-series
// cancel parity. When a recurring master is CANCELLED, even a still-CONFIRMED
// override instance is suppressed from display/alarms/free-busy (all of which
// flow through expansion). This is a deliberate behavior, not an accident. An
// instance of a cancelled series that remains must not linger.
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

	// Both expansion paths run the same merge engine and must drop the cancelled
	// Apr 2 occurrence, leaving exactly Apr 1 and Apr 3.
	for _, tc := range []struct {
		name string
		days func() ([]int, error)
	}{
		{"ListExpandedByDateRange", func() ([]int, error) {
			events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
			days := make([]int, len(events))
			for i, e := range events {
				days[i] = e.StartTime.Day()
			}
			return days, err
		}},
		{"ListExpandedEvents", func() ([]int, error) {
			events, err := recurSvc.ListExpandedEvents(ctx, from, to)
			days := make([]int, len(events))
			for i, e := range events {
				days[i] = e.InstanceTime.Day()
			}
			return days, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			days, err := tc.days()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			// Should get 2 instances (Apr 1, Apr 3). Apr 2 is cancelled.
			if len(days) != 2 {
				t.Fatalf("got %d events (days %v), want 2", len(days), days)
			}
			for _, d := range days {
				if d == 2 {
					t.Error("cancelled Apr 2 instance appeared in results")
				}
			}
		})
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

func TestListFilteredEvents_AttachesAttendees(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	oneOff, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "One-off Sync",
		StartTime:  time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}
	if err := eventsSvc.ReplaceAttendees(ctx, oneOff.ID, []model.Attendee{
		{Email: "me@example.com", Name: "Me", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
		{Email: "alice@example.com", Name: "Alice", RSVPStatus: "ACCEPTED", Role: "REQ-PARTICIPANT"},
	}); err != nil {
		t.Fatalf("replace one-off attendees: %v", err)
	}

	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	if err != nil {
		t.Fatalf("create recurring: %v", err)
	}
	if err := eventsSvc.ReplaceAttendees(ctx, master.ID, []model.Attendee{
		{Email: "me@example.com", Name: "Me", RSVPStatus: "TENTATIVE", Role: "REQ-PARTICIPANT"},
	}); err != nil {
		t.Fatalf("replace master attendees: %v", err)
	}

	events, err := recurSvc.ListFilteredEvents(ctx, EventListParams{
		From: time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListFilteredEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 generated occurrence", len(events))
	}
	got := events[0]
	if got.Title != "Weekly Sync" {
		t.Fatalf("title = %q, want Weekly Sync", got.Title)
	}
	if !got.StartTime.Equal(time.Date(2026, 4, 28, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %s, want generated 2026-04-28 11:00", got.StartTime)
	}
	if len(got.Attendees) != 1 || got.Attendees[0].Email != "me@example.com" || got.Attendees[0].RSVPStatus != "TENTATIVE" {
		t.Fatalf("generated attendees = %#v, want me@example.com TENTATIVE", got.Attendees)
	}

	listed, err := recurSvc.ListFilteredEvents(ctx, EventListParams{
		From: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListFilteredEvents DTSTART window: %v", err)
	}
	var foundOneOff bool
	for _, e := range listed {
		if e.ID != oneOff.ID {
			continue
		}
		foundOneOff = true
		if len(e.Attendees) != 2 {
			t.Fatalf("one-off attendees = %#v, want 2", e.Attendees)
		}
	}
	if !foundOneOff {
		t.Fatal("missing one-off in DTSTART window")
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

// TestMovedOverride_WiderWindowNoDuplication verifies a window that spans both
// the original slot and the moved day. It yields the moved occurrence once (not
// the suppressed Apr 6 slot, and not a duplicate). The series' other
// untouched occurrences sit alongside it.
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
