package storage

import "context"

const todoCategoryExists = "EXISTS (SELECT 1 FROM todo_categories tc WHERE tc.todo_id = todos.id AND tc.category = ?)"

// CompletedFilter selects todos by completion state. The zero value
// (CompletionAny) adds no clause.
type CompletedFilter int64

const (
	// CompletionAny keeps completed and open todos.
	CompletionAny CompletedFilter = iota
	// CompletedOnly keeps todos with a completion time.
	CompletedOnly
	// OpenOnly keeps todos without a completion time.
	OpenOnly
)

// TodoFilterParams holds optional filters for todo queries.
// Zero values mean "no filter" for that field.
type TodoFilterParams struct {
	CalendarID   int64
	FilterStatus string
	Category     string
	// HideCompleted, when true, hides COMPLETED and CANCELLED todos.
	HideCompleted bool
	// CompletedFilter selects rows by completed_at. CompletedOnly keeps
	// only completed todos, and OpenOnly keeps only open todos.
	CompletedFilter CompletedFilter
	FromDate        string
	ToDate          string
	// IncludeDeleted, when true, omits the default `deleted_at IS NULL`
	// filter. Callers that need to see soft-deleted rows (trash views,
	// --include-deleted flag) set this to true.
	IncludeDeleted bool
	// DeletedOnly, when true, inverts the default filter to
	// `deleted_at IS NOT NULL`. Implies IncludeDeleted.
	DeletedOnly bool
}

// addTodoFilters appends the soft-delete, calendar, status, category, and
// date clauses shared by every todo query, in one fixed order.
func (w *whereBuilder) addTodoFilters(arg TodoFilterParams) {
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	if arg.Category != "" {
		w.add(todoCategoryExists, arg.Category)
	}
	if arg.HideCompleted {
		w.add("status != 'COMPLETED' AND status != 'CANCELLED'")
	}
	switch arg.CompletedFilter {
	case CompletedOnly:
		w.add("completed_at IS NOT NULL")
	case OpenOnly:
		w.add("completed_at IS NULL")
	}
	// FromDate and ToDate form the half-open range [FromDate, ToDate).
	// The filter includes a todo due on FromDate. The filter excludes a
	// todo due on ToDate. A todo with no due date passes the filter.
	if arg.FromDate != "" {
		w.add("(due_date IS NULL OR due_date >= ?)", arg.FromDate)
	}
	if arg.ToDate != "" {
		w.add("(due_date IS NULL OR due_date < ?)", arg.ToDate)
	}
}

func (q *Queries) ListTodosFiltered(ctx context.Context, arg TodoFilterParams) ([]Todo, error) {
	var w whereBuilder
	// Non-recurring, non-RDATE-only masters (RDATE-only handled by ListRecurringTodosFiltered).
	w.add("recurrence_rule IS NULL AND (rdates IS NULL OR rdates = '') AND recurrence_id = ''")
	w.addTodoFilters(arg)
	where, args := w.build()
	return q.queryTodos(ctx, where, args, "due_date ASC, summary ASC")
}

func (q *Queries) ListRecurringTodosFiltered(ctx context.Context, arg TodoFilterParams) ([]Todo, error) {
	var w whereBuilder
	// RRULE masters and RDATE-only masters (no RRULE but has RDATEs); both need expansion.
	w.add("(recurrence_rule IS NOT NULL OR (rdates IS NOT NULL AND rdates != '')) AND recurrence_id = ''")
	w.addTodoFilters(arg)
	where, args := w.build()
	return q.queryTodos(ctx, where, args, "due_date ASC, summary ASC")
}

func (q *Queries) ListTodosForExport(ctx context.Context, arg TodoFilterParams) ([]Todo, error) {
	var w whereBuilder
	w.addTodoFilters(arg)
	where, args := w.build()
	return q.queryTodos(ctx, where, args, "due_date ASC, summary ASC")
}
