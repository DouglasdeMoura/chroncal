package recurrence

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// localDays returns the window of the local days [first, last], as the CLI
// builds it from --from and --to.
func localDays(first, last time.Time) (time.Time, time.Time) {
	from := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, time.Local)
	to := time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, time.Local)
	return from, to
}

func day(m time.Month, d int) time.Time {
	return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC)
}

// TestDateOnlyRangeFilters_EveryZone guards issue #875. A date-only value
// is a floating date. A range of local days holds the values of those
// dates, and no other dates, in every zone. The test covers a single
// all-day event, a recurring all-day event, a recurring date-only todo, and
// a recurring date-only journal.
func TestDateOnlyRangeFilters_EveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		db, q := testutil.NewTestDB(t)
		ctx := context.Background()
		svc := NewService(db, q)

		events := event.NewService(db, q)
		if _, err := events.Create(ctx, event.CreateParams{
			CalendarID: 1, Title: "Holiday", AllDay: true,
			StartTime: day(10, 28), EndTime: day(10, 29),
		}); err != nil {
			t.Fatalf("create event: %v", err)
		}
		if _, err := events.Create(ctx, event.CreateParams{
			CalendarID: 1, Title: "Weekly", AllDay: true,
			StartTime: day(10, 21), EndTime: day(10, 22),
			RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
		}); err != nil {
			t.Fatalf("create recurring event: %v", err)
		}
		if _, err := todo.NewService(db, q).Create(ctx, todo.CreateParams{
			CalendarID: 1, Summary: "Water plants", DueDate: "2026-10-24",
			RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
		}); err != nil {
			t.Fatalf("create todo: %v", err)
		}
		if _, err := journal.NewService(db, q).Create(ctx, journal.CreateParams{
			CalendarID: 1, Summary: "Log", StartDate: "2026-10-24",
			RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
		}); err != nil {
			t.Fatalf("create journal: %v", err)
		}

		// Each case names a range of local days and the dates it must hold.
		eventCases := []struct {
			first, last time.Time
			want        []string
		}{
			{day(10, 27), day(10, 27), nil},
			{day(10, 28), day(10, 28), []string{"Holiday 2026-10-28", "Weekly 2026-10-28"}},
			{day(10, 21), day(10, 21), []string{"Weekly 2026-10-21"}},
			{day(10, 22), day(10, 27), nil},
		}
		for _, tc := range eventCases {
			from, to := localDays(tc.first, tc.last)

			listed, err := svc.ListFilteredEvents(ctx, EventListParams{From: from, To: to})
			if err != nil {
				t.Fatalf("ListFilteredEvents: %v", err)
			}
			var got []string
			for _, e := range listed {
				got = append(got, e.Title+" "+e.StartTime.UTC().Format(time.DateOnly))
			}
			assertSameSet(t, "ListFilteredEvents "+from.Format(time.DateOnly), got, tc.want)

			expanded, err := svc.ListExpandedEvents(ctx, from, to)
			if err != nil {
				t.Fatalf("ListExpandedEvents: %v", err)
			}
			got = nil
			for _, e := range expanded {
				got = append(got, e.Title+" "+e.InstanceTime.UTC().Format(time.DateOnly))
			}
			assertSameSet(t, "ListExpandedEvents "+from.Format(time.DateOnly), got, tc.want)
		}

		seriesCases := []struct {
			first, last time.Time
			want        []string
		}{
			{day(10, 24), day(11, 30), []string{"2026-10-24", "2026-10-31", "2026-11-07"}},
			{day(10, 30), day(10, 30), nil},
			{day(10, 31), day(10, 31), []string{"2026-10-31"}},
		}
		for _, tc := range seriesCases {
			from, to := localDays(tc.first, tc.last)

			todos, err := svc.ListFilteredTodos(ctx, TodoListParams{From: from, To: to})
			if err != nil {
				t.Fatalf("ListFilteredTodos: %v", err)
			}
			var got []string
			for _, td := range todos {
				got = append(got, td.DueDate)
			}
			assertSameSet(t, "ListFilteredTodos "+from.Format(time.DateOnly), got, tc.want)

			journals, err := svc.ListFilteredJournals(ctx, JournalListParams{From: from, To: to})
			if err != nil {
				t.Fatalf("ListFilteredJournals: %v", err)
			}
			got = nil
			for _, j := range journals {
				got = append(got, j.StartDate)
			}
			assertSameSet(t, "ListFilteredJournals "+from.Format(time.DateOnly), got, tc.want)
		}
	})
}

func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	counts := make(map[string]int, len(want))
	for _, w := range want {
		counts[w]++
	}
	for _, g := range got {
		counts[g]--
	}
	for k, n := range counts {
		if n != 0 {
			t.Fatalf("%s: got %v, want %v (mismatch at %q)", label, got, want, k)
		}
	}
}

// TestDateOnlySort_EveryZone guards issue #875 for the order of a list. An
// all-day event of October 28 sorts after a timed event at 22:00 local on
// October 27. A date-only todo of October 28 sorts after a timed todo due
// at 22:00 local on October 27.
func TestDateOnlySort_EveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		db, q := testutil.NewTestDB(t)
		ctx := context.Background()
		svc := NewService(db, q)
		evening := time.Date(2026, 10, 27, 22, 0, 0, 0, time.Local)

		events := event.NewService(db, q)
		if _, err := events.Create(ctx, event.CreateParams{
			CalendarID: 1, Title: "Holiday", AllDay: true,
			StartTime: day(10, 28), EndTime: day(10, 29),
		}); err != nil {
			t.Fatalf("create event: %v", err)
		}
		if _, err := events.Create(ctx, event.CreateParams{
			CalendarID: 1, Title: "Dinner",
			StartTime: evening, EndTime: evening.Add(time.Hour),
		}); err != nil {
			t.Fatalf("create event: %v", err)
		}
		todos := todo.NewService(db, q)
		if _, err := todos.Create(ctx, todo.CreateParams{CalendarID: 1, Summary: "Date", DueDate: "2026-10-28"}); err != nil {
			t.Fatalf("create todo: %v", err)
		}
		if _, err := todos.Create(ctx, todo.CreateParams{CalendarID: 1, Summary: "Timed", DueDate: evening.UTC().Format(time.RFC3339)}); err != nil {
			t.Fatalf("create todo: %v", err)
		}

		from, to := localDays(day(10, 27), day(10, 28))
		listed, err := svc.ListFilteredEvents(ctx, EventListParams{From: from, To: to})
		if err != nil {
			t.Fatalf("ListFilteredEvents: %v", err)
		}
		if len(listed) != 2 || listed[0].Title != "Dinner" || listed[1].Title != "Holiday" {
			t.Fatalf("events = %v, want [Dinner Holiday]", titles(listed))
		}
		expanded, err := svc.ListExpandedByDateRange(ctx, from, to)
		if err != nil {
			t.Fatalf("ListExpandedByDateRange: %v", err)
		}
		if len(expanded) != 2 || expanded[0].Title != "Dinner" || expanded[1].Title != "Holiday" {
			t.Fatalf("expanded = %v, want [Dinner Holiday]", titles(expanded))
		}
		listedTodos, err := svc.ListFilteredTodos(ctx, TodoListParams{})
		if err != nil {
			t.Fatalf("ListFilteredTodos: %v", err)
		}
		if len(listedTodos) != 2 || listedTodos[0].Summary != "Timed" || listedTodos[1].Summary != "Date" {
			t.Fatalf("todos = %+v, want [Timed Date]", listedTodos)
		}
	})
}

func titles(events []event.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Title)
	}
	return out
}
