package journal

import (
	"context"
	"database/sql"
	"testing"

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

// The category child-write is forced to fail by passing duplicate categories:
// journal_categories has PRIMARY KEY (journal_id, category) and
// ParseCategoryList does not dedupe, so inserting "dup,dup" fails on the second
// row. That isolates the failure to the category child-write while the parent
// journal row write succeeds — exactly the partial-failure window the fix must
// close. (Dropping journal_categories would instead break the UpdateJournal
// statement itself: SQLite compiles the journals_fts_au trigger, which reads
// journal_categories, at prepare time.)

// TestCreate_AtomicOnCategoryFailure asserts that when the category child-write
// fails, Create returns an error AND leaves no journal row behind. Before the
// fix the journal row was committed in autocommit before categories ran, so a
// failure left an orphan row (the mirror of issue #73).
func TestCreate_AtomicOnCategoryFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Summary:    "Atomic Create",
		StartDate:  "2026-04-01",
		Categories: "dup,dup",
	})
	if err == nil {
		t.Fatal("Create succeeded, want error from duplicate category write")
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM journals`); n != 0 {
		t.Fatalf("journal rows persisted after failed Create = %d, want 0", n)
	}
}

// TestUpdate_AtomicOnCategoryFailure asserts that when the category child-write
// fails during Update, the original journal row and its categories are left
// unchanged. Before the fix the journal row was updated in autocommit before
// categories ran, so a failure produced a half-updated, category-wiped row.
func TestUpdate_AtomicOnCategoryFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	orig, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Summary:    "Original Summary",
		StartDate:  "2026-04-01",
		Categories: "work,urgent",
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	catsBefore := countRows(t, db,
		`SELECT COUNT(*) FROM journal_categories WHERE journal_id = ?`, orig.ID)
	if catsBefore != 2 {
		t.Fatalf("seed categories = %d, want 2", catsBefore)
	}

	_, err = svc.Update(ctx, orig.ID, UpdateParams{
		CalendarID: 1,
		Summary:    "Changed Summary",
		StartDate:  "2026-04-02",
		Categories: "dup,dup",
	})
	if err == nil {
		t.Fatal("Update succeeded, want error from duplicate category write")
	}

	// The row write must have rolled back: summary is unchanged.
	got, err := svc.Get(ctx, orig.ID)
	if err != nil {
		t.Fatalf("get after failed update: %v", err)
	}
	if got.Summary != "Original Summary" {
		t.Fatalf("summary after failed Update = %q, want %q", got.Summary, "Original Summary")
	}
	// Categories must be intact too: the replace rolled back.
	if n := countRows(t, db,
		`SELECT COUNT(*) FROM journal_categories WHERE journal_id = ?`, orig.ID); n != 2 {
		t.Fatalf("categories after failed Update = %d, want 2", n)
	}
}

// TestUpsertByUID_AtomicOnCategoryFailure asserts that when the category
// child-write fails, UpsertByUID returns an error AND leaves no journal row
// behind. Before the fix the upsert committed the row in autocommit and only
// then ran ReplaceCategories in a separate transaction, so a failure left an
// orphan row.
func TestUpsertByUID_AtomicOnCategoryFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	_, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:        "upsert-atomic-uid",
		CalendarID: 1,
		Summary:    "Atomic Upsert",
		StartDate:  "2026-04-01",
		Categories: "dup,dup",
	})
	if err == nil {
		t.Fatal("UpsertByUID succeeded, want error from duplicate category write")
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM journals`); n != 0 {
		t.Fatalf("journal rows persisted after failed UpsertByUID = %d, want 0", n)
	}
}
