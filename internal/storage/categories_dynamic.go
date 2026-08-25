package storage

import "context"

// ListCategoriesByEventIDs returns categories for the given event IDs. The ids
// are de-duplicated and chunked so the IN clause never exceeds SQLite's
// host-parameter cap on wide recurrence expansions (issue #303).
// Hand-written because database/sql does not support slice parameters.
func (q *Queries) ListCategoriesByEventIDs(ctx context.Context, ids []int64) ([]EventCategory, error) {
	return loadByIDChunks(ctx, ids, func(ctx context.Context, chunk []int64) ([]EventCategory, error) {
		placeholders, args := expandInPlaceholders(chunk)
		query := "SELECT event_id, category FROM event_categories WHERE event_id IN (" + placeholders + ") ORDER BY event_id, category"

		rows, err := q.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var items []EventCategory
		for rows.Next() {
			var i EventCategory
			if err := rows.Scan(&i.EventID, &i.Category); err != nil {
				return nil, err
			}
			items = append(items, i)
		}
		return items, rows.Err()
	})
}

// ListCategoriesByTodoIDs returns categories for the given todo IDs. See
// ListCategoriesByEventIDs for the de-dup/chunk rationale.
// Hand-written because database/sql does not support slice parameters.
func (q *Queries) ListCategoriesByTodoIDs(ctx context.Context, ids []int64) ([]TodoCategory, error) {
	return loadByIDChunks(ctx, ids, func(ctx context.Context, chunk []int64) ([]TodoCategory, error) {
		placeholders, args := expandInPlaceholders(chunk)
		query := "SELECT todo_id, category FROM todo_categories WHERE todo_id IN (" + placeholders + ") ORDER BY todo_id, category"

		rows, err := q.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var items []TodoCategory
		for rows.Next() {
			var i TodoCategory
			if err := rows.Scan(&i.TodoID, &i.Category); err != nil {
				return nil, err
			}
			items = append(items, i)
		}
		return items, rows.Err()
	})
}

// ListCategoriesByJournalIDs returns categories for the given journal IDs. See
// ListCategoriesByEventIDs for the de-dup/chunk rationale.
// Hand-written because database/sql does not support slice parameters.
func (q *Queries) ListCategoriesByJournalIDs(ctx context.Context, ids []int64) ([]JournalCategory, error) {
	return loadByIDChunks(ctx, ids, func(ctx context.Context, chunk []int64) ([]JournalCategory, error) {
		placeholders, args := expandInPlaceholders(chunk)
		query := "SELECT journal_id, category FROM journal_categories WHERE journal_id IN (" + placeholders + ") ORDER BY journal_id, category"

		rows, err := q.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var items []JournalCategory
		for rows.Next() {
			var i JournalCategory
			if err := rows.Scan(&i.JournalID, &i.Category); err != nil {
				return nil, err
			}
			items = append(items, i)
		}
		return items, rows.Err()
	})
}
