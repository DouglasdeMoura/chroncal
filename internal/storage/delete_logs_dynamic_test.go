package storage

import (
	"context"
	"strings"
	"testing"
)

// TestDeleteLogsNamingRows_MatchExactIDs: the lookups of the logs that name a
// row match the whole ID between the delimiters. Row 1 never matches row 12
// or row 21. An occurrence entry with no row ("2:") names no row. A log of
// another UID never matches. A list above the parameter limit of SQLite
// works, because the IDs go in one JSON value. The in-package test cannot
// import internal/testutil (an import cycle), so it opens an in-memory
// database, as TestEventsUIDIndexIsUsedByLookups does.
func TestDeleteLogsNamingRows_MatchExactIDs(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO event_series_deletes (id, calendar_id, uid, hidden_ids) VALUES (1, 1, 'u', '12,21'), (2, 1, 'u', '3,1'), (3, 1, 'other', '1')`,
		`INSERT INTO event_truncate_deletes (id, calendar_id, uid, cutoff_time, previous_rrule, hidden_ids) VALUES (1, 1, 'u', 'a', '', '12'), (2, 1, 'u', 'b', '', '1'), (3, 1, 'u', 'c', '', NULL)`,
		`INSERT INTO event_exdate_deletes (id, calendar_id, uid, recurrence_id, hidden_ids) VALUES (1, 1, 'u', 'a', '1:12,2:'), (2, 1, 'u', 'b', '1:5,2:1'), (3, 1, 'u', 'c', '1:21')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	series, err := q.ListEventSeriesDeletesNamingRows(ctx, "u", []int64{1})
	if err != nil || len(series) != 1 || series[0].ID != 2 {
		t.Errorf("series logs naming row 1 = %+v, %v; want log 2", series, err)
	}
	truncs, err := q.ListEventTruncateDeletesNamingRows(ctx, "u", []int64{1})
	if err != nil || len(truncs) != 1 || truncs[0].ID != 2 {
		t.Errorf("truncation logs naming row 1 = %+v, %v; want log 2", truncs, err)
	}
	occ, err := q.ListEventExdateDeletesNamingRows(ctx, "u", []int64{1})
	if err != nil || len(occ) != 1 || occ[0].ID != 2 {
		t.Errorf("occurrence logs naming row 1 = %+v, %v; want log 2", occ, err)
	}
	if occ, err := q.ListEventExdateDeletesNamingRows(ctx, "u", []int64{2}); err != nil || len(occ) != 0 {
		t.Errorf("occurrence logs naming row 2 = %+v, %v; want none (\"2:\" is an entry with no row)", occ, err)
	}
	many := make([]int64, 40000)
	for i := range many {
		many[i] = int64(100000 + i)
	}
	many[len(many)-1] = 21
	if series, err := q.ListEventSeriesDeletesNamingRows(ctx, "u", many); err != nil || len(series) != 1 || series[0].ID != 1 {
		t.Errorf("series logs naming a long list = %+v, %v; want log 1", series, err)
	}
	for _, query := range []string{listEventSeriesDeletesNamingRows, listEventTruncateDeletesNamingRows, listEventExdateDeletesNamingRows} {
		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, "u", "[1]")
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatalf("scan plan: %v", err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		if !strings.Contains(strings.Join(plan, "\n"), "USING INDEX idx_event_") {
			t.Errorf("plan of %q reads no uid index:\n%s", query[:40], strings.Join(plan, "\n"))
		}
	}
}
