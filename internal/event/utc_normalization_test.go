package event

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// storedTimes reads the raw start_time / end_time strings persisted for an
// event, so tests can assert the on-disk representation rather than the parsed
// time.Time (which would hide a stored offset).
func storedTimes(t *testing.T, db *sql.DB, id int64) (string, string) {
	t.Helper()
	var start, end string
	if err := db.QueryRowContext(context.Background(),
		"SELECT start_time, end_time FROM events WHERE id = ?", id,
	).Scan(&start, &end); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	return start, end
}

// occursOn reports how many times the event with the given id appears in the
// half-open UTC date-range [from, to).
func occursOn(t *testing.T, svc *Service, id int64, from, to time.Time) int {
	t.Helper()
	events, err := svc.ListByDateRange(context.Background(), from, to)
	if err != nil {
		t.Fatalf("list by date range: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.ID == id {
			n++
		}
	}
	return n
}

// TestCreate_NormalizesTimedToUTC reproduces issue #254 for timed events: when
// the caller supplies start/end times carrying a non-UTC offset, the row must
// be stored as RFC 3339 in UTC. Otherwise the persisted string sorts
// incorrectly against the UTC "Z" bounds used by date-range queries (SQLite
// compares TEXT lexicographically) and the event silently disappears from list
// views.
func TestCreate_NormalizesTimedToUTC(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	// 23:00 at UTC-4 == 03:00 the next day in UTC.
	loc := time.FixedZone("EDT", -4*60*60)
	start := time.Date(2026, 6, 26, 23, 0, 0, 0, loc)
	end := start.Add(time.Hour)

	created, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Offset Event",
		StartTime:  start,
		EndTime:    end,
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	rawStart, rawEnd := storedTimes(t, db, created.ID)
	if !strings.HasSuffix(rawStart, "Z") {
		t.Errorf("stored start_time = %q, want UTC (suffix Z)", rawStart)
	}
	if !strings.HasSuffix(rawEnd, "Z") {
		t.Errorf("stored end_time = %q, want UTC (suffix Z)", rawEnd)
	}

	// The event (03:00-04:00Z on Jun 27) must appear in the Jun 27 UTC window.
	from := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	if got := occursOn(t, svc, created.ID, from, to); got != 1 {
		t.Errorf("timed event appears %d times in [%s, %s), want 1",
			got, from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
}

// TestCreate_AllDayPinsToUTCMidnight guards against a naive .UTC() coercion of
// all-day events. The CLI builds all-day events at local midnight; a plain
// .UTC() in a positive-offset zone would shift them onto the previous UTC day
// and make them surface on two calendar days. All-day events must be stored at
// UTC midnight on their wall-clock date and occupy exactly one day.
func TestCreate_AllDayPinsToUTCMidnight(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	loc := time.FixedZone("JST", 9*60*60)
	start := time.Date(2026, 12, 25, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)

	created, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Holiday",
		StartTime:  start,
		EndTime:    end,
		AllDay:     true,
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	rawStart, rawEnd := storedTimes(t, db, created.ID)
	if rawStart != "2026-12-25T00:00:00Z" {
		t.Errorf("stored start_time = %q, want 2026-12-25T00:00:00Z", rawStart)
	}
	if rawEnd != "2026-12-26T00:00:00Z" {
		t.Errorf("stored end_time = %q, want 2026-12-26T00:00:00Z", rawEnd)
	}

	dec24 := occursOn(t, svc, created.ID,
		time.Date(2026, 12, 24, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC))
	dec25 := occursOn(t, svc, created.ID,
		time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 26, 0, 0, 0, 0, time.UTC))
	if dec24 != 0 || dec25 != 1 {
		t.Errorf("all-day event occurrences: Dec24=%d Dec25=%d, want 0 and 1", dec24, dec25)
	}
}

// TestUpdate_NormalizesToUTC confirms the edit path enforces the same
// invariant: re-saving with offset-bearing times stores them in UTC.
func TestUpdate_NormalizesToUTC(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	created, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Event",
		StartTime:  time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	loc := time.FixedZone("EDT", -4*60*60)
	if _, err := svc.Update(ctx, created.ID, UpdateParams{
		CalendarID: 1,
		Title:      "Event",
		StartTime:  time.Date(2026, 6, 26, 23, 0, 0, 0, loc),
		EndTime:    time.Date(2026, 6, 27, 0, 0, 0, 0, loc),
	}); err != nil {
		t.Fatalf("update event: %v", err)
	}

	rawStart, rawEnd := storedTimes(t, db, created.ID)
	if !strings.HasSuffix(rawStart, "Z") || !strings.HasSuffix(rawEnd, "Z") {
		t.Errorf("stored times after update = %q / %q, want UTC (suffix Z)", rawStart, rawEnd)
	}
}
