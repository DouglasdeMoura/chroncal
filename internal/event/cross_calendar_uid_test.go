package event

import (
	"context"
	"testing"
	"time"
)

func insertCalendar(t *testing.T, svc *Service, name string) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := svc.db.ExecContext(ctx, `INSERT INTO calendars (name) VALUES (?)`, name)
	if err != nil {
		t.Fatalf("insert calendar %q: %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("calendar id: %v", err)
	}
	return id
}

// TestUpsertByUID_SameUIDOnTwoCalendars is the issue #756 contract. Google
// Calendar (and CalDAV) reuse one UID when the same meeting lives on more
// than one collection. A pull of the second calendar must insert a second
// row. It must not move or overwrite the first calendar's copy.
func TestUpsertByUID_SameUIDOnTwoCalendars(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	otherID := insertCalendar(t, svc, "Other")

	start := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	p := UpsertParams{
		UID: "shared-meeting", CalendarID: 1, Title: "Standup",
		StartTime: start, EndTime: end,
	}
	first, err := svc.UpsertByUID(ctx, p)
	if err != nil {
		t.Fatalf("upsert calendar 1: %v", err)
	}

	p.CalendarID = otherID
	p.Title = "Standup (other)"
	second, err := svc.UpsertByUID(ctx, p)
	if err != nil {
		t.Fatalf("upsert calendar 2: %v", err)
	}

	if first.ID == second.ID {
		t.Fatalf("upsert reused id %d; each calendar must keep its own copy", first.ID)
	}
	if first.CalendarID != 1 {
		t.Errorf("first.CalendarID = %d, want 1", first.CalendarID)
	}
	if second.CalendarID != otherID {
		t.Errorf("second.CalendarID = %d, want %d", second.CalendarID, otherID)
	}

	got1, err := svc.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get first: %v", err)
	}
	if got1.CalendarID != 1 || got1.Title != "Standup" {
		t.Errorf("first copy: calendar %d title %q, want calendar 1 title Standup",
			got1.CalendarID, got1.Title)
	}
	got2, err := svc.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if got2.CalendarID != otherID || got2.Title != "Standup (other)" {
		t.Errorf("second copy: calendar %d title %q, want calendar %d title Standup (other)",
			got2.CalendarID, got2.Title, otherID)
	}

	listed, err := svc.ListByDateRange(ctx, start.Add(-time.Minute), end.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListByDateRange: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("ListByDateRange returned %d events, want 2 (one per calendar)", len(listed))
	}
}

func TestUpsertByUID_SameCalendarStillUpdates(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	p := UpsertParams{
		UID: "same-cal", CalendarID: 1, Title: "Original",
		StartTime: start, EndTime: start.Add(time.Hour),
	}
	first, err := svc.UpsertByUID(ctx, p)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	p.Title = "Updated"
	second, err := svc.UpsertByUID(ctx, p)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("same-calendar upsert created id %d, want %d", second.ID, first.ID)
	}
	if second.Title != "Updated" {
		t.Errorf("Title = %q, want Updated", second.Title)
	}
}
