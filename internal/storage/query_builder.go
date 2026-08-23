package storage

import (
	"context"
	"strings"
)

// whereBuilder accumulates AND-joined WHERE conditions with positional
// parameters for dynamic SQL queries.
type whereBuilder struct {
	clauses []string
	args    []interface{}
}

func (w *whereBuilder) add(clause string, args ...interface{}) {
	w.clauses = append(w.clauses, clause)
	w.args = append(w.args, args...)
}

func (w *whereBuilder) build() (string, []interface{}) {
	if len(w.clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(w.clauses, " AND "), w.args
}

// addSoftDeleteFilter appends the canonical deleted_at clause shared by every
// dynamic read path (events, todos, journals). Default: hide soft-deleted
// rows. Callers that need to see them (trash views, --include-deleted) set
// includeDeleted; deletedOnly inverts the filter to surface only trashed rows.
func (w *whereBuilder) addSoftDeleteFilter(includeDeleted, deletedOnly bool) {
	switch {
	case deletedOnly:
		w.add("deleted_at IS NOT NULL")
	case !includeDeleted:
		w.add("deleted_at IS NULL")
	}
}

// expandInPlaceholders builds the "?,?,?" placeholder list and the corresponding
// positional args slice for a SQL `IN (...)` clause over the given int64 ids.
// database/sql has no native slice-parameter support, so every hand-written
// IN query needs this. Callers must ensure len(ids) > 0.
func expandInPlaceholders(ids []int64) (string, []interface{}) {
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return placeholders, args
}

// sqliteINBatch bounds how many values go into a single `IN (...)` batched
// query (category/attendee loaders, overrides by UID). SQLite (modernc) caps
// host parameters at 32766. Stay well under that: the IN clause then never
// overflows on wide expansions. The cost is a few extra queries instead of one.
const sqliteINBatch = 500

// dedupeInt64s returns ids with duplicates removed. It keeps first-seen
// order. Recurrence expansion feeds one id per expanded instance, so the same
// master row id repeats once per occurrence. Collapse of the duplicates
// shrinks the IN-clause loaders from O(instances) back to O(distinct rows).
func dedupeInt64s(ids []int64) []int64 {
	if len(ids) < 2 {
		return ids
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// loadByIDChunks de-duplicates ids and runs load once per chunk that fits under
// SQLite's host-parameter cap. It concatenates the results. Each chunk passed to
// load is small enough for a single `IN (...)` query.
func loadByIDChunks[T any](ctx context.Context, ids []int64, load func(context.Context, []int64) ([]T, error)) ([]T, error) {
	var out []T
	for _, chunk := range chunkSlice(dedupeInt64s(ids), sqliteINBatch) {
		rows, err := load(ctx, chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// expandStringPlaceholders is the string analogue of expandInPlaceholders.
// It builds the "?,?,?" list and the corresponding positional args for a SQL `IN (...)`
// clause over string values (for example UIDs). Callers must ensure len(vals) > 0.
func expandStringPlaceholders(vals []string) (string, []interface{}) {
	placeholders := strings.Repeat("?,", len(vals))
	placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
	args := make([]interface{}, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	return placeholders, args
}

func (q *Queries) queryEvents(ctx context.Context, where string, args []interface{}, orderBy string) ([]Event, error) {
	query := "SELECT * FROM events " + where + " ORDER BY " + orderBy
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if err := checkScanColumns(rows, "events", eventColumns); err != nil {
		rows.Close()
		return nil, err
	}
	return scanEvents(rows)
}

func (q *Queries) queryTodos(ctx context.Context, where string, args []interface{}, orderBy string) ([]Todo, error) {
	query := "SELECT * FROM todos " + where + " ORDER BY " + orderBy
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if err := checkScanColumns(rows, "todos", todoColumns); err != nil {
		rows.Close()
		return nil, err
	}
	return scanTodos(rows)
}

func (q *Queries) queryJournals(ctx context.Context, where string, args []interface{}, orderBy string) ([]Journal, error) {
	query := "SELECT * FROM journals " + where + " ORDER BY " + orderBy
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if err := checkScanColumns(rows, "journals", journalColumns); err != nil {
		rows.Close()
		return nil, err
	}
	return scanJournals(rows)
}
