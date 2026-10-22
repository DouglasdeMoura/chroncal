package storage

import (
	"context"
	"strings"
	"testing"
)

// TestPurgeQueriesSearchTheUIDIndex: the purge queries read the view
// event_series_rows with bound UIDs. SQLite then filters each arm of the view
// by an index. No step of the plan scans the events table. A filter that
// joins the view to the masters (uid = m.uid) scans events once for each
// master. The in-package test cannot import internal/testutil (an import
// cycle), so it opens an in-memory database, as
// TestEventsUIDIndexIsUsedByLookups does.
func TestPurgeQueriesSearchTheUIDIndex(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		query string
		args  []any
	}{
		{"ListSeriesOverridesOfUIDs", strings.Replace(listSeriesOverridesOfUIDs, "/*SLICE:uids*/?", "?, ?", 1), []any{"a", "b"}},
		{"ListTrashedMastersBefore", listTrashedMastersBefore, []any{"2026-01-01T00:00:00Z"}},
	} {
		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tc.query, tc.args...)
		if err != nil {
			t.Fatalf("%s: explain: %v", tc.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatalf("%s: scan: %v", tc.name, err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		for _, step := range plan {
			// The view rows are the scan of the co-routine (s); a scan of
			// a table alias of events is the regression.
			if strings.HasPrefix(step, "SCAN ") && step != "SCAN s" {
				t.Errorf("%s: plan step %q scans a table; plan:\n%s", tc.name, step, strings.Join(plan, "\n"))
			}
		}
	}
}
