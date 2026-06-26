package storage

import (
	"context"
	"testing"
)

// TestListCategoriesByEventIDs_OverParameterCap reproduces issue #303: the
// category batch loader built a single unbounded `IN (?,?,…)` clause. When the
// recurrence expander feeds one id per expanded instance, the slice can exceed
// SQLite's 32766 host-parameter cap, tripping "too many SQL variables". The
// loader must chunk the IN clause (as the override loader already does) so a
// wide id slice still succeeds.
func TestListCategoriesByEventIDs_OverParameterCap(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	evt := insertEventRow(t, q, ctx, "uid-cat", "", "2026-01-01T09:00:00Z")
	if _, err := q.CreateEventCategory(ctx, CreateEventCategoryParams{
		EventID:  evt.ID,
		Category: "work",
	}); err != nil {
		t.Fatalf("CreateEventCategory: %v", err)
	}

	// Build an id slice that overflows SQLite's 32766 parameter cap. The real
	// event id is included so a correct, chunked loader still returns its
	// category; the remaining ids are distinct and non-existent so chunking
	// (not just de-duplication) is exercised.
	const n = 40000
	ids := make([]int64, 0, n)
	ids = append(ids, evt.ID)
	for i := int64(1); int64(len(ids)) < n; i++ {
		if i == evt.ID {
			continue
		}
		ids = append(ids, i)
	}

	cats, err := q.ListCategoriesByEventIDs(ctx, ids)
	if err != nil {
		t.Fatalf("ListCategoriesByEventIDs with %d ids: %v", len(ids), err)
	}
	if len(cats) != 1 || cats[0].EventID != evt.ID || cats[0].Category != "work" {
		t.Fatalf("got %+v, want one {EventID:%d, Category:work}", cats, evt.ID)
	}
}
