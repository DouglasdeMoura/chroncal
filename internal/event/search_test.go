package event

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/storage"
	"github.com/douglasdemoura/tcal/internal/testutil"
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

func mustCreate(t *testing.T, svc *Service, ctx context.Context, p CreateParams) Event {
	t.Helper()
	e, err := svc.Create(ctx, p)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return e
}

func TestService_Search(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	calID := testCalendar(t, q)

	mustCreate(t, svc, ctx, CreateParams{
		CalendarID:  calID,
		Title:       "Budget Meeting Q1",
		Description: "Review quarterly budget",
		Location:    "Conference Room A",
		StartTime:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})
	mustCreate(t, svc, ctx, CreateParams{
		CalendarID:  calID,
		Title:       "Team Lunch",
		Description: "Budget-friendly options",
		Location:    "Cafeteria",
		StartTime:   time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 2, 13, 0, 0, 0, time.UTC),
		Categories:  "social",
	})
	mustCreate(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Title:      "Sprint Planning",
		StartTime:  time.Date(2026, 4, 3, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC),
		Categories: "work",
	})

	tests := []struct {
		name    string
		params  SearchParams
		wantLen int
	}{
		{"title and description match", SearchParams{Query: "budget"}, 2},
		{"location match", SearchParams{Query: "Conference"}, 1},
		{"category match", SearchParams{Query: "social"}, 1},
		{"no results", SearchParams{Query: "nonexistent"}, 0},
		{"date range filter", SearchParams{Query: "budget", From: "2026-04-02T00:00:00Z"}, 1},
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

	mustCreate(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Title:      "Q1 Meeting",
		Categories: "work,q1",
		StartTime:  time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})
	mustCreate(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Title:      "Q2 Planning",
		Categories: "work,q2",
		StartTime:  time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC),
	})

	tests := []struct {
		name    string
		params  ExportParams
		wantLen int
	}{
		{"export all", ExportParams{}, 2},
		{"export by category", ExportParams{Category: "q1"}, 1},
		{"export by date range", ExportParams{From: "2026-06-01T00:00:00Z", To: "2026-08-01T00:00:00Z"}, 1},
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
