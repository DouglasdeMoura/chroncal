package storage

import (
	"context"
	"testing"
)

// insertEventRow inserts an event row (master or override) for the batch
// override-query tests. recurrenceID == "" marks a master.
func insertEventRow(t *testing.T, q *Queries, ctx context.Context, uid, recurrenceID, start string) Event {
	t.Helper()
	evt, err := q.CreateEvent(ctx, CreateEventParams{
		Uid:          uid,
		CalendarID:   1,
		Title:        "T",
		StartTime:    start,
		EndTime:      start,
		Status:       "CONFIRMED",
		Transp:       "OPAQUE",
		Class:        "PUBLIC",
		RecurrenceID: recurrenceID,
	})
	if err != nil {
		t.Fatalf("CreateEvent(%s,%s): %v", uid, recurrenceID, err)
	}
	return evt
}

func TestListOverridesByUIDs_GroupsAndFilters(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Two recurring masters, each with overrides; plus a third master that has
	// none, a non-override row, and a soft-deleted override that must be excluded.
	insertEventRow(t, q, ctx, "uid-a", "", "2026-01-01T09:00:00Z")
	insertEventRow(t, q, ctx, "uid-a", "2026-01-08T09:00:00Z", "2026-01-08T11:00:00Z")
	insertEventRow(t, q, ctx, "uid-a", "2026-01-15T09:00:00Z", "2026-01-15T11:00:00Z")
	insertEventRow(t, q, ctx, "uid-b", "", "2026-01-02T09:00:00Z")
	insertEventRow(t, q, ctx, "uid-b", "2026-01-09T09:00:00Z", "2026-01-09T11:00:00Z")
	insertEventRow(t, q, ctx, "uid-c", "", "2026-01-03T09:00:00Z") // master only, no overrides

	deleted := insertEventRow(t, q, ctx, "uid-a", "2026-01-22T09:00:00Z", "2026-01-22T11:00:00Z")
	if _, err := db.ExecContext(ctx, "UPDATE events SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?", deleted.ID); err != nil {
		t.Fatalf("soft-delete override: %v", err)
	}

	rows, err := q.ListOverridesByUIDs(ctx, []string{"uid-a", "uid-b", "uid-c"})
	if err != nil {
		t.Fatalf("ListOverridesByUIDs: %v", err)
	}

	byUID := map[string]int{}
	for _, r := range rows {
		if r.RecurrenceID == "" {
			t.Errorf("row %d is a master, not an override", r.ID)
		}
		if r.Uid == "uid-c" {
			t.Errorf("uid-c has no overrides but one was returned")
		}
		byUID[r.Uid]++
	}

	// uid-a: 2 live overrides (the soft-deleted one excluded); uid-b: 1.
	if byUID["uid-a"] != 2 {
		t.Errorf("uid-a overrides = %d, want 2 (soft-deleted excluded)", byUID["uid-a"])
	}
	if byUID["uid-b"] != 1 {
		t.Errorf("uid-b overrides = %d, want 1", byUID["uid-b"])
	}
	if len(rows) != 3 {
		t.Errorf("total overrides = %d, want 3", len(rows))
	}
}

func TestListOverridesByUIDs_EmptyInput(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	rows, err := q.ListOverridesByUIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListOverridesByUIDs(nil): %v", err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil for empty input", rows)
	}
}
