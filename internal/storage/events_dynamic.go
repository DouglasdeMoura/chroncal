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
}

const eventCategoryExists = "EXISTS (SELECT 1 FROM event_categories ec WHERE ec.event_id = events.id AND ec.category = ?)"

func (w *whereBuilder) addEventFilters(arg EventFilterParams) {
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
	w.add("recurrence_rule IS NULL AND recurrence_id = ''")
	w.addEventFilters(arg)
	where, args := w.build()
	return q.queryEvents(ctx, where, args, "start_time ASC")
}

func (q *Queries) ListRecurringEventsFiltered(ctx context.Context, arg EventFilterParams) ([]Event, error) {
	var w whereBuilder
	w.add("recurrence_rule IS NOT NULL AND recurrence_id = ''")
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
