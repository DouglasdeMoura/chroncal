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

// expandInPlaceholders builds the "?,?,?" placeholder list and the matching
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

// expandStringPlaceholders is the string analogue of expandInPlaceholders,
// building the "?,?,?" list and matching positional args for a SQL `IN (...)`
// clause over string values (e.g. UIDs). Callers must ensure len(vals) > 0.
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
	return scanEvents(rows)
}

func (q *Queries) queryTodos(ctx context.Context, where string, args []interface{}, orderBy string) ([]Todo, error) {
	query := "SELECT * FROM todos " + where + " ORDER BY " + orderBy
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
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
	return scanJournals(rows)
}
