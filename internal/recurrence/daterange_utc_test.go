package recurrence

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestListFilteredEvents_NonUTCBoundsLexicalComparison guards issue #305: CLI
// date-range bounds arrive as time.Time values in a non-UTC location (the CLI
// builds them in time.Local). They must be normalized to UTC before being
// formatted and compared lexically against the UTC-stored start/end strings,
// otherwise the local offset in the RFC3339 string breaks the comparison near
// window edges.
//
// The bound is built in a fixed -03:00 zone so the test is deterministic
// regardless of the host timezone (CI runs in UTC).
func TestListFilteredEvents_NonUTCBoundsLexicalComparison(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	// Event entirely before the requested window: it ends 2026-04-01T02:00:00Z,
	// which is 2026-03-31 23:00 in -03:00 local time — before 2026-04-01.
	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Before window",
		StartTime:  time.Date(2026, 4, 1, 1, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	// Window opens 2026-04-01 00:00 in -03:00 (= 2026-04-01T03:00:00Z). The
	// event ends at 02:00Z, before the window opens, so it must NOT appear.
	loc := time.FixedZone("test-0300", -3*60*60)
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 30)

	events, err := recurSvc.ListFilteredEvents(ctx, EventListParams{
		From: from,
		To:   to,
	})
	if err != nil {
		t.Fatalf("ListFilteredEvents: %v", err)
	}

	if len(events) != 0 {
		t.Fatalf("got %d events, want 0 (event ends before window; local-offset bound leaked into lexical comparison)", len(events))
	}
}

// TestExportExpandedByDateRange_NonUTCBoundsLexicalComparison is the export-path
// (ical export) sibling of the test above: the same local-offset bound must be
// normalized to UTC before the SQL string comparison.
func TestExportExpandedByDateRange_NonUTCBoundsLexicalComparison(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	_, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Before window",
		StartTime:  time.Date(2026, 4, 1, 1, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	loc := time.FixedZone("test-0300", -3*60*60)
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 30)

	events, err := recurSvc.ExportExpandedByDateRange(ctx, ExportFilterParams{
		From: from,
		To:   to,
	})
	if err != nil {
		t.Fatalf("ExportExpandedByDateRange: %v", err)
	}

	if len(events) != 0 {
		t.Fatalf("got %d events, want 0 (event ends before window; local-offset bound leaked into lexical comparison)", len(events))
	}
}
