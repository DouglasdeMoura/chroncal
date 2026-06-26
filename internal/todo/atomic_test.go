package todo

import (
	"context"
	"database/sql"
	"testing"
	"time"

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

// A duplicate category isolates the failure to the category child-write:
// todo_categories has PRIMARY KEY (todo_id, category) and ParseCategoryList
// does not dedupe, so inserting "dup,dup" fails on the second row while the
// parent todo row write succeeds — exactly the partial-failure window the fix
// must close. Dropping todo_categories instead would break CreateTodo /
// UpdateTodo themselves: the todos FTS triggers read todo_categories at
// statement time.
const dupCategories = "dup,dup"

// TestCreate_AtomicOnCategoryFailure asserts that when the category child-write
// fails, Create returns an error AND leaves no todo row behind. Before the fix
// the todo row was committed in autocommit before categories ran in a separate
// transaction, so a failure left an orphan row (mirror of event issue #73).
func TestCreate_AtomicOnCategoryFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Summary:    "Atomic Create",
		DueDate:    time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		Categories: dupCategories,
	})
	if err == nil {
		t.Fatal("Create succeeded, want error from duplicate category write")
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM todos`); n != 0 {
		t.Fatalf("todo rows persisted after failed Create = %d, want 0", n)
	}
}

// TestUpdate_AtomicOnCategoryFailure asserts that when the category child-write
// fails during Update, the original todo row and its categories are left
// unchanged AND the resource is not marked dirty. Before the fix the todo row
// was updated in autocommit before categories ran, so a failure produced a
// half-updated row while MarkResourceDirty never fired — a CalDAV-linked todo
// silently lost the edit.
func TestUpdate_AtomicOnCategoryFailure(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	orig, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Summary:    "Original Summary",
		DueDate:    time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		Categories: "work,urgent",
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	catsBefore := countRows(t, db,
		`SELECT COUNT(*) FROM todo_categories WHERE todo_id = ?`, orig.ID)
	if catsBefore != 2 {
		t.Fatalf("seed categories = %d, want 2", catsBefore)
	}

	_, err = svc.Update(ctx, orig.ID, UpdateParams{
		CalendarID: 1,
		Summary:    "Changed Summary",
		DueDate:    time.Date(2026, 4, 2, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		Categories: dupCategories,
	})
	if err == nil {
		t.Fatal("Update succeeded, want error from duplicate category write")
	}

	// The row write must have rolled back: summary and categories unchanged.
	got, err := svc.Get(ctx, orig.ID)
	if err != nil {
		t.Fatalf("get after failed update: %v", err)
	}
	if got.Summary != "Original Summary" {
		t.Fatalf("summary after failed Update = %q, want %q", got.Summary, "Original Summary")
	}
	if n := countRows(t, db,
		`SELECT COUNT(*) FROM todo_categories WHERE todo_id = ?`, orig.ID); n != 2 {
		t.Fatalf("categories after failed Update = %d, want 2 (unchanged)", n)
	}
}

// TestUpsertByUID_AtomicOnCategoryFailure asserts that when the category
// child-write fails, UpsertByUID returns an error AND leaves no todo row
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
		DueDate:    time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
		Categories: dupCategories,
	})
	if err == nil {
		t.Fatal("UpsertByUID succeeded, want error from duplicate category write")
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM todos`); n != 0 {
		t.Fatalf("todo rows persisted after failed UpsertByUID = %d, want 0", n)
	}
}
