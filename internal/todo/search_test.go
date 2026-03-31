package todo

import (
	"context"
	"testing"

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
