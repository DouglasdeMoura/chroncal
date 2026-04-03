package journal

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

func mustCreateJournal(t *testing.T, svc *Service, ctx context.Context, p CreateParams) Journal {
	t.Helper()
	j, err := svc.Create(ctx, p)
	if err != nil {
		t.Fatalf("create journal: %v", err)
	}
	return j
}

func TestJournalService_Search(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	calID := testCalendar(t, q)

	mustCreateJournal(t, svc, ctx, CreateParams{
		CalendarID:  calID,
		Summary:     "Sprint retrospective",
		Description: "Team discussed blockers and wins",
		Categories:  "work",
	})
	mustCreateJournal(t, svc, ctx, CreateParams{
		CalendarID:  calID,
		Summary:     "Personal reflections",
		Description: "Thinking about goals",
		Categories:  "personal",
	})
	mustCreateJournal(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "Meeting notes on blockers",
		Categories: "work",
	})

	tests := []struct {
		name    string
		params  SearchParams
		wantLen int
	}{
		{"summary and description match", SearchParams{Query: "blockers"}, 2},
		{"category match", SearchParams{Query: "personal"}, 1},
		{"no results", SearchParams{Query: "nonexistent"}, 0},
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
