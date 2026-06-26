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

// TestJournalService_ExportFiltered_DateRange covers issue #429: export
// --from/--to must actually restrict journals by start date instead of being a
// silent no-op.
func TestJournalService_ExportFiltered_DateRange(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	calID := testCalendar(t, q)

	mustCreateJournal(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "January entry",
		StartDate:  "2026-01-15",
	})
	mustCreateJournal(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "June entry",
		StartDate:  "2026-06-15",
	})
	mustCreateJournal(t, svc, ctx, CreateParams{
		CalendarID: calID,
		Summary:    "No start date",
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
		{"window excludes both dated journals", ExportParams{From: "2026-03-01", To: "2026-04-01"}, 1}, // dateless only
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
