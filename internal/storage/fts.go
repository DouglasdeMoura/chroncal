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

func (q *Queries) SearchEventsFTS(ctx context.Context, query string, calendarID int64, fromTime, toTime, filterStatus string) ([]Event, error) {
	var w whereBuilder
	w.add("id IN (SELECT rowid FROM events_fts WHERE events_fts MATCH ?)", query)
	if calendarID != 0 {
		w.add("calendar_id = ?", calendarID)
	}
	if fromTime != "" {
		w.add("end_time > ?", fromTime)
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

func (q *Queries) SearchJournalsFTS(ctx context.Context, query string, calendarID int64, filterStatus string) ([]Journal, error) {
	var w whereBuilder
	w.add("id IN (SELECT rowid FROM journals_fts WHERE journals_fts MATCH ?)", query)
	if calendarID != 0 {
		w.add("calendar_id = ?", calendarID)
	}
	if filterStatus != "" {
		w.add("status = ?", filterStatus)
	}
	where, args := w.build()
	return q.queryJournals(ctx, where, args, "start_date ASC, summary ASC")
}
