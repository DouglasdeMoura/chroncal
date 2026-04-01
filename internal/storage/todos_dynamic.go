package storage

import "context"

const todoCategoryExists = "EXISTS (SELECT 1 FROM todo_categories tc WHERE tc.todo_id = todos.id AND tc.category = ?)"

type ListTodosFilteredParams struct {
	CalendarID    int64
	FilterStatus  string
	HideCompleted int64
	FromDate      string
	ToDate        string
}

func (q *Queries) ListTodosFiltered(ctx context.Context, arg ListTodosFilteredParams) ([]Todo, error) {
	var w whereBuilder
	w.add("recurrence_rule IS NULL AND recurrence_id = ''")
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	if arg.HideCompleted != 0 {
		w.add("status != 'COMPLETED' AND status != 'CANCELLED'")
	}
	if arg.FromDate != "" {
		w.add("(due_date IS NULL OR due_date >= ?)", arg.FromDate)
	}
	if arg.ToDate != "" {
		w.add("(due_date IS NULL OR due_date < ?)", arg.ToDate)
	}
	where, args := w.build()
	return q.queryTodos(ctx, where, args, "due_date ASC, summary ASC")
}

type ListRecurringTodosFilteredParams struct {
	CalendarID    int64
	FilterStatus  string
	HideCompleted int64
}

func (q *Queries) ListRecurringTodosFiltered(ctx context.Context, arg ListRecurringTodosFilteredParams) ([]Todo, error) {
	var w whereBuilder
	w.add("recurrence_rule IS NOT NULL AND recurrence_id = ''")
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	if arg.HideCompleted != 0 {
		w.add("status != 'COMPLETED' AND status != 'CANCELLED'")
	}
	where, args := w.build()
	return q.queryTodos(ctx, where, args, "due_date ASC, summary ASC")
}

type ListTodosForExportParams struct {
	CalendarID      int64
	Category        string
	FilterStatus    string
	CompletedFilter int64
}

func (q *Queries) ListTodosForExport(ctx context.Context, arg ListTodosForExportParams) ([]Todo, error) {
	var w whereBuilder
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.Category != "" {
		w.add(todoCategoryExists, arg.Category)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	if arg.CompletedFilter == 1 {
		w.add("completed_at IS NOT NULL")
	} else if arg.CompletedFilter == 2 {
		w.add("completed_at IS NULL")
	}
	where, args := w.build()
	return q.queryTodos(ctx, where, args, "due_date ASC, summary ASC")
}
