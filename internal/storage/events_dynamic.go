package storage

import "context"

// EventFilterParams holds optional filters for event queries.
// Zero values mean "no filter" for that field.
type EventFilterParams struct {
	CalendarID   int64
	FilterStatus string
	Category     string
	FromTime     string
	ToTime       string
	// IncludeDeleted, when true, omits the default `deleted_at IS NULL`
	// filter. Callers that need to see soft-deleted rows (trash views,
	// --include-deleted flag) set this to true.
	IncludeDeleted bool
	// DeletedOnly, when true, inverts the default filter to
	// `deleted_at IS NOT NULL`. Implies IncludeDeleted.
	DeletedOnly bool
}

const eventCategoryExists = "EXISTS (SELECT 1 FROM event_categories ec WHERE ec.event_id = events.id AND ec.category = ?)"

func (w *whereBuilder) addEventFilters(arg EventFilterParams) {
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	if arg.Category != "" {
		w.add(eventCategoryExists, arg.Category)
	}
	if arg.FromTime != "" {
		w.add("end_time > ?", arg.FromTime)
	}
	if arg.ToTime != "" {
		w.add("start_time < ?", arg.ToTime)
	}
}

func (q *Queries) ListEventsFiltered(ctx context.Context, arg EventFilterParams) ([]Event, error) {
	var w whereBuilder
	// Non-recurring, non-RDATE-only masters (RDATE-only handled by ListRecurringEventsFiltered).
	w.add("recurrence_rule IS NULL AND (rdates IS NULL OR rdates = '') AND recurrence_id = ''")
	w.addEventFilters(arg)
	where, args := w.build()
	return q.queryEvents(ctx, where, args, "start_time ASC")
}

func (q *Queries) ListRecurringEventsFiltered(ctx context.Context, arg EventFilterParams) ([]Event, error) {
	var w whereBuilder
	// RRULE masters and RDATE-only masters (no RRULE but has RDATEs); both need expansion.
	w.add("(recurrence_rule IS NOT NULL OR (rdates IS NOT NULL AND rdates != '')) AND recurrence_id = ''")
	w.addEventFilters(arg)
	where, args := w.build()
	return q.queryEvents(ctx, where, args, "start_time ASC")
}

func (q *Queries) ListEventsForExport(ctx context.Context, arg EventFilterParams) ([]Event, error) {
	var w whereBuilder
	w.addEventFilters(arg)
	where, args := w.build()
	return q.queryEvents(ctx, where, args, "start_time ASC")
}
