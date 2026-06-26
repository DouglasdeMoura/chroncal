package todo

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

func testCalendar(t *testing.T, q *storage.Queries) int64 {
	t.Helper()
	cal, err := q.CreateCalendar(context.Background(), storage.CreateCalendarParams{
		Name: "test-cal", Color: "#FF0000",
	})
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	return cal.ID
}

func mustCreateTodo(t *testing.T, svc *Service, ctx context.Context, p CreateParams) Todo {
	t.Helper()
	td, err := svc.Create(ctx, p)
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	return td
}

func TestService_Search(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	calID := testCalendar(t, q)

	mustCreateTodo(t, svc, ctx, CreateParams{
		CalendarID:  calID,
		Summary:     "Buy groceries",
		Description: "Milk, eggs, budget items",
		Categories:  "personal",
	})
	mustCreateTodo(t, svc, ctx, CreateParams{
		CalendarID:  calID,
		Summary:     "Review budget proposal",
		Description: "Q1 financial review",
		Categories:  "work",
	})
	mustCreateTodo(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "Clean garage",
		Categories: "personal",
	})

	tests := []struct {
		name    string
		params  SearchParams
		wantLen int
	}{
		{"summary and description match", SearchParams{Query: "budget"}, 2},
		{"category match", SearchParams{Query: "personal"}, 2},
		{"no results", SearchParams{Query: "nonexistent"}, 0},
		{"filter incomplete only", SearchParams{Query: "budget", Completed: 2}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := svc.Search(ctx, tt.params)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(results) != tt.wantLen {
				t.Errorf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}

func TestService_Search_NoGhostAfterCalendarDelete(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	calSvc := calendar.NewService(db, q)
	todoSvc := NewService(db, q)
	ctx := context.Background()

	tempCal, err := calSvc.Create(ctx, "FTS todo temp", "#000", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	const unique = "TodoGhostbusterFTS2026"
	mustCreateTodo(t, todoSvc, ctx, CreateParams{
		CalendarID: tempCal.ID,
		Summary:    unique,
	})

	before, err := todoSvc.Search(ctx, SearchParams{Query: unique})
	if err != nil {
		t.Fatalf("search before delete: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("before delete: got %d hits, want 1", len(before))
	}

	if err := calSvc.Delete(ctx, tempCal.ID); err != nil {
		t.Fatalf("delete calendar: %v", err)
	}

	after, err := todoSvc.Search(ctx, SearchParams{Query: unique})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("after delete: got %d FTS hits, want 0 (orphan rows)", len(after))
	}
}

func TestService_ExportFiltered(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	calID := testCalendar(t, q)

	mustCreateTodo(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "Work task 1",
		Categories: "work",
	})
	mustCreateTodo(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "Personal task",
		Categories: "personal",
	})

	tests := []struct {
		name    string
		params  ExportParams
		wantLen int
	}{
		{"export all", ExportParams{}, 2},
		{"export by category", ExportParams{Category: "work"}, 1},
		{"export incomplete only", ExportParams{Completed: 2}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := svc.ExportFiltered(ctx, tt.params)
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			if len(results) != tt.wantLen {
				t.Errorf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}

// TestService_ExportFiltered_DateRange covers issue #429: export --from/--to
// must actually restrict todos by due date instead of being a silent no-op.
func TestService_ExportFiltered_DateRange(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	calID := testCalendar(t, q)

	mustCreateTodo(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "January task",
		DueDate:    "2026-01-15",
	})
	mustCreateTodo(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "June task",
		DueDate:    "2026-06-15",
	})
	mustCreateTodo(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "No due date",
	})

	tests := []struct {
		name    string
		params  ExportParams
		wantLen int
	}{
		{"no range exports all", ExportParams{}, 3},
		{"from bound only", ExportParams{From: "2026-03-01"}, 2}, // June + dateless
		{"to bound only", ExportParams{To: "2026-03-01"}, 2},     // January + dateless
		{"narrow window around June", ExportParams{From: "2026-06-01", To: "2026-07-01"}, 2},
		{"window excludes both dated todos", ExportParams{From: "2026-03-01", To: "2026-04-01"}, 1}, // dateless only
		{"lower boundary is inclusive", ExportParams{From: "2026-06-15", To: "2026-07-01"}, 2},
		{"upper boundary is exclusive", ExportParams{From: "2026-01-01", To: "2026-06-15"}, 2}, // January + dateless
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := svc.ExportFiltered(ctx, tt.params)
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			if len(results) != tt.wantLen {
				t.Errorf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}
