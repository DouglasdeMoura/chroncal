package storage

import "context"

const todoCategoryExists = "EXISTS (SELECT 1 FROM todo_categories tc WHERE tc.todo_id = todos.id AND tc.category = ?)"

// addTodoListFilters appends the calendar / status / hide-completed clauses
// shared verbatim by ListTodosFiltered and ListRecurringTodosFiltered, in the
// same order so positional args line up.
func (w *whereBuilder) addTodoListFilters(calendarID int64, filterStatus string, hideCompleted int64) {
	if calendarID != 0 {
		w.add("calendar_id = ?", calendarID)
	}
	if filterStatus != "" {
		w.add("status = ?", filterStatus)
	}
	if hideCompleted != 0 {
		w.add("status != 'COMPLETED' AND status != 'CANCELLED'")
	}
}

type ListTodosFilteredParams struct {
	CalendarID     int64
	FilterStatus   string
	HideCompleted  int64
	FromDate       string
	ToDate         string
	IncludeDeleted bool
	DeletedOnly    bool
}

func (q *Queries) ListTodosFiltered(ctx context.Context, arg ListTodosFilteredParams) ([]Todo, error) {
	var w whereBuilder
	w.add("recurrence_rule IS NULL AND recurrence_id = ''")
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
	w.addTodoListFilters(arg.CalendarID, arg.FilterStatus, arg.HideCompleted)
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
	CalendarID     int64
	FilterStatus   string
	HideCompleted  int64
	IncludeDeleted bool
	DeletedOnly    bool
}

func (q *Queries) ListRecurringTodosFiltered(ctx context.Context, arg ListRecurringTodosFilteredParams) ([]Todo, error) {
	var w whereBuilder
	w.add("recurrence_rule IS NOT NULL AND recurrence_id = ''")
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
	w.addTodoListFilters(arg.CalendarID, arg.FilterStatus, arg.HideCompleted)
	where, args := w.build()
	return q.queryTodos(ctx, where, args, "due_date ASC, summary ASC")
}

type ListTodosForExportParams struct {
	CalendarID      int64
	Category        string
	FilterStatus    string
	CompletedFilter int64
	IncludeDeleted  bool
	DeletedOnly     bool
}

func (q *Queries) ListTodosForExport(ctx context.Context, arg ListTodosForExportParams) ([]Todo, error) {
	var w whereBuilder
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
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
