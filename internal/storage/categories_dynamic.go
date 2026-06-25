package storage

import "context"

// ListCategoriesByEventIDs returns categories for the given event IDs.
// Hand-written because database/sql does not support slice parameters.
func (q *Queries) ListCategoriesByEventIDs(ctx context.Context, ids []int64) ([]EventCategory, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := expandInPlaceholders(ids)
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
}

// ListCategoriesByTodoIDs returns categories for the given todo IDs.
// Hand-written because database/sql does not support slice parameters.
func (q *Queries) ListCategoriesByTodoIDs(ctx context.Context, ids []int64) ([]TodoCategory, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := expandInPlaceholders(ids)
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
}

// ListCategoriesByJournalIDs returns categories for the given journal IDs.
// Hand-written because database/sql does not support slice parameters.
func (q *Queries) ListCategoriesByJournalIDs(ctx context.Context, ids []int64) ([]JournalCategory, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := expandInPlaceholders(ids)
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
}
