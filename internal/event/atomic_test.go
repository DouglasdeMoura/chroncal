package event

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// countRows returns the number of rows matching the given query.
func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// dropCategoriesTable removes the event_categories table so the category
// child-write fails inside Create/Update, simulating a partial failure.
func dropCategoriesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DROP TABLE event_categories`); err != nil {
		t.Fatalf("drop event_categories: %v", err)
	}
}

// TestCreate_AtomicOnCategoryFailure asserts that when the category child-write
// fails, Create returns an error AND leaves no event row behind. Before the fix
// the event row was committed in autocommit before categories ran, so a failure
// left an orphan row (the duplicate-on-retry bug from issue #73).
func TestCreate_AtomicOnCategoryFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	dropCategoriesTable(t, db)

	_, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Atomic Create",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Categories: "work,urgent",
	})
	if err == nil {
		t.Fatal("Create succeeded, want error from failing category write")
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM events`); n != 0 {
		t.Fatalf("event rows persisted after failed Create = %d, want 0", n)
	}
}

// TestUpdate_AtomicOnCategoryFailure asserts that when the category child-write
// fails during Update, the original event row and its categories are left
// unchanged. Before the fix the event row was updated in autocommit before
// categories ran, so a failure produced a half-updated, category-wiped row.
func TestUpdate_AtomicOnCategoryFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	orig, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Original Title",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Categories: "work,urgent",
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	catsBefore := countRows(t, db,
		`SELECT COUNT(*) FROM event_categories WHERE event_id = ?`, orig.ID)
	if catsBefore != 2 {
		t.Fatalf("seed categories = %d, want 2", catsBefore)
	}

	dropCategoriesTable(t, db)

	_, err = svc.Update(ctx, orig.ID, UpdateParams{
		CalendarID: 1,
		Title:      "Changed Title",
		StartTime:  time.Date(2026, 4, 2, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC),
		Categories: "different",
	})
	if err == nil {
		t.Fatal("Update succeeded, want error from failing category write")
	}

	// The row write must have rolled back: title is unchanged.
	got, err := svc.Get(ctx, orig.ID)
	if err != nil {
		t.Fatalf("get after failed update: %v", err)
	}
	if got.Title != "Original Title" {
		t.Fatalf("title after failed Update = %q, want %q", got.Title, "Original Title")
	}
	if !got.StartTime.Equal(orig.StartTime) {
		t.Fatalf("start time after failed Update = %v, want %v", got.StartTime, orig.StartTime)
	}
}

// TestUpsertByUID_AtomicOnCategoryFailure asserts that when the category
// child-write fails, UpsertByUID returns an error AND leaves no event row
// behind. Before the fix the upsert committed the row in autocommit and only
// then ran ReplaceCategories in a separate transaction, so a failure left an
// orphan row (the sibling of issue #73, filed as issue #87).
//
// The failure is injected via duplicate categories: event_categories has a
// PRIMARY KEY (event_id, category) and ParseCategoryList does not dedupe, so
// inserting "dup,dup" fails on the second row. That isolates the failure to the
// category child-write while the parent upsert row write succeeds — exactly the
// partial-failure window the fix must close. (Dropping event_categories instead
// would break the upsert statement itself: SQLite compiles the upsert's
// ON CONFLICT UPDATE-branch trigger, which reads event_categories, at prepare
// time.)
func TestUpsertByUID_AtomicOnCategoryFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	_, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:        "upsert-atomic-uid",
		CalendarID: 1,
		Title:      "Atomic Upsert",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Categories: "dup,dup",
	})
	if err == nil {
		t.Fatal("UpsertByUID succeeded, want error from duplicate category write")
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM events`); n != 0 {
		t.Fatalf("event rows persisted after failed UpsertByUID = %d, want 0", n)
	}
}

// TestUpdateWithRelations_AtomicOnAttendeeFailure asserts that when the
// attendee child-write fails, UpdateWithRelations returns an error AND leaves
// the original event row unchanged. The TUI edit path previously called Update
// and then ReplaceAttendees/ReplaceAlarms in separate transactions, so a
// failure after the row update left a half-updated row (issue #87).
//
// Dropping event_attendees isolates the failure to the attendee write: no
// trigger on events references that table, so the event row UPDATE still runs;
// only the in-tx attendee replacement fails, which must roll the row back.
func TestUpdateWithRelations_AtomicOnAttendeeFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	orig, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Original Title",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE event_attendees`); err != nil {
		t.Fatalf("drop event_attendees: %v", err)
	}

	_, err = svc.UpdateWithRelations(ctx, orig.ID, UpdateParams{
		CalendarID: 1,
		Title:      "Changed Title",
		StartTime:  time.Date(2026, 4, 2, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC),
	},
		[]model.Attendee{{Email: "a@example.com", Role: "REQ-PARTICIPANT", RSVPStatus: "NEEDS-ACTION"}},
		nil,
	)
	if err == nil {
		t.Fatal("UpdateWithRelations succeeded, want error from failing attendee write")
	}

	got, err := svc.Get(ctx, orig.ID)
	if err != nil {
		t.Fatalf("get after failed update: %v", err)
	}
	if got.Title != "Original Title" {
		t.Fatalf("title after failed UpdateWithRelations = %q, want %q", got.Title, "Original Title")
	}
	if !got.StartTime.Equal(orig.StartTime) {
		t.Fatalf("start time after failed UpdateWithRelations = %v, want %v", got.StartTime, orig.StartTime)
	}
}
