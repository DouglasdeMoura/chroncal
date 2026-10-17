package storage

import (
	"context"
	"strings"
	"testing"
)

// TestEventsUIDIndexIsUsedByLookups guards issue #843. Migration 050 replaced
// UNIQUE(uid, recurrence_id) with UNIQUE(calendar_id, uid, recurrence_id), so
// the remaining unique index cannot serve a lookup by uid alone. Every edit,
// sync pull, and alarm check runs one of these queries, so each must reach
// events through idx_events_uid instead of scanning the table.
func TestEventsUIDIndexIsUsedByLookups(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	cases := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "GetEventByUID",
			query: "SELECT * FROM events WHERE uid = ? AND recurrence_id = '' AND deleted_at IS NULL",
			args:  []any{"uid-x"},
		},
		{
			name:  "ListEventMastersByUIDIncludingDeleted",
			query: "SELECT * FROM events WHERE uid = ? AND recurrence_id = '' ORDER BY id",
			args:  []any{"uid-x"},
		},
		{
			name:  "ListEventsByUIDAndRecurrenceID",
			query: "SELECT * FROM events WHERE uid = ? AND recurrence_id = ? AND deleted_at IS NULL",
			args:  []any{"uid-x", "2026-01-08T09:00:00Z"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tc.query, tc.args...)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			defer rows.Close()
			var plan []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatalf("scan plan: %v", err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("plan rows: %v", err)
			}
			joined := strings.Join(plan, " | ")
			if strings.Contains(joined, "SCAN events") {
				t.Errorf("%s scans events: %s", tc.name, joined)
			}
			if !strings.Contains(joined, "idx_events_uid") {
				t.Errorf("%s does not use idx_events_uid: %s", tc.name, joined)
			}
		})
	}
}
