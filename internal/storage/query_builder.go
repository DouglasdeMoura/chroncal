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
