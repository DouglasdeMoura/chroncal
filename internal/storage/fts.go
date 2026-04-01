package storage

import (
	"context"
	"strings"
)

// FTSQuery converts a user search string into safe FTS5 query syntax.
// Each word gets quoted and suffixed with * for prefix matching, approximating
// the old LIKE '%word%' behaviour at token boundaries.
func FTSQuery(input string) string {
	words := strings.Fields(input)
	if len(words) == 0 {
		return ""
	}
	parts := make([]string, len(words))
	for i, w := range words {
		w = strings.ReplaceAll(w, "\"", "\"\"")
		parts[i] = "\"" + w + "\"" + "*"
	}
	return strings.Join(parts, " ")
}

// --- Event FTS operations ---

func (q *Queries) UpsertEventFTS(ctx context.Context, id int64, title, description, location, categories string) error {
	if _, err := q.db.ExecContext(ctx, "DELETE FROM events_fts WHERE rowid = ?", id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx,
		"INSERT INTO events_fts (rowid, title, description, location, categories) VALUES (?, ?, ?, ?, ?)",
		id, title, description, location, categories)
	return err
}

func (q *Queries) DeleteEventFTS(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, "DELETE FROM events_fts WHERE rowid = ?", id)
	return err
}

func (q *Queries) DeleteEventsFTSByUID(ctx context.Context, uid string) error {
	_, err := q.db.ExecContext(ctx,
		"DELETE FROM events_fts WHERE rowid IN (SELECT id FROM events WHERE uid = ?)", uid)
	return err
}

// --- Todo FTS operations ---

func (q *Queries) UpsertTodoFTS(ctx context.Context, id int64, summary, description, location, categories string) error {
	if _, err := q.db.ExecContext(ctx, "DELETE FROM todos_fts WHERE rowid = ?", id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx,
		"INSERT INTO todos_fts (rowid, summary, description, location, categories) VALUES (?, ?, ?, ?, ?)",
		id, summary, description, location, categories)
	return err
}

func (q *Queries) DeleteTodoFTS(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, "DELETE FROM todos_fts WHERE rowid = ?", id)
	return err
}

func (q *Queries) DeleteTodosFTSByUID(ctx context.Context, uid string) error {
	_, err := q.db.ExecContext(ctx,
		"DELETE FROM todos_fts WHERE rowid IN (SELECT id FROM todos WHERE uid = ?)", uid)
	return err
}

// --- Search ---

func (q *Queries) SearchEventsFTS(ctx context.Context, query string, calendarID int64, fromTime, toTime, filterStatus string) ([]Event, error) {
	var w whereBuilder
	w.add("id IN (SELECT rowid FROM events_fts WHERE events_fts MATCH ?)", query)
	if calendarID != 0 {
		w.add("calendar_id = ?", calendarID)
	}
	if fromTime != "" {
		w.add("start_time >= ?", fromTime)
	}
	if toTime != "" {
		w.add("start_time < ?", toTime)
	}
	if filterStatus != "" {
		w.add("status = ?", filterStatus)
	}
	where, args := w.build()
	return q.queryEvents(ctx, where, args, "start_time ASC")
}

func (q *Queries) SearchTodosFTS(ctx context.Context, query string, calendarID int64, filterStatus string, completedFilter int64) ([]Todo, error) {
	var w whereBuilder
	w.add("id IN (SELECT rowid FROM todos_fts WHERE todos_fts MATCH ?)", query)
	if calendarID != 0 {
		w.add("calendar_id = ?", calendarID)
	}
	if filterStatus != "" {
		w.add("status = ?", filterStatus)
	}
	if completedFilter == 1 {
		w.add("completed_at IS NOT NULL")
	} else if completedFilter == 2 {
		w.add("completed_at IS NULL")
	}
	where, args := w.build()
	return q.queryTodos(ctx, where, args, "due_date ASC, summary ASC")
}

// --- Rebuild / sync ---

func (q *Queries) RebuildEventsFTS(ctx context.Context) error {
	if _, err := q.db.ExecContext(ctx, "DELETE FROM events_fts"); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO events_fts (rowid, title, description, location, categories)
		SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
		       COALESCE((SELECT GROUP_CONCAT(ec.category, ' ') FROM event_categories ec WHERE ec.event_id = e.id), '')
		FROM events e`)
	return err
}

func (q *Queries) RebuildTodosFTS(ctx context.Context) error {
	if _, err := q.db.ExecContext(ctx, "DELETE FROM todos_fts"); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO todos_fts (rowid, summary, description, location, categories)
		SELECT t.id, t.summary, COALESCE(t.description, ''), COALESCE(t.location, ''),
		       COALESCE((SELECT GROUP_CONCAT(tc.category, ' ') FROM todo_categories tc WHERE tc.todo_id = t.id), '')
		FROM todos t`)
	return err
}
