package storage

import "context"

const journalCategoryExists = "EXISTS (SELECT 1 FROM journal_categories jc WHERE jc.journal_id = journals.id AND jc.category = ?)"

// JournalFilterParams holds optional filters for journal queries.
// Zero values mean "no filter" for that field.
type JournalFilterParams struct {
	CalendarID   int64
	FilterStatus string
	Category     string
	// HideCancelled, when true, hides CANCELLED journals.
	HideCancelled bool
	FromDate      string
	ToDate        string
	// IncludeDeleted, when true, omits the default `deleted_at IS NULL`
	// filter. Callers that need to see soft-deleted rows (trash views,
	// --include-deleted flag) set this to true.
	IncludeDeleted bool
	// DeletedOnly, when true, inverts the default filter to
	// `deleted_at IS NOT NULL`. Implies IncludeDeleted.
	DeletedOnly bool
}

// addJournalFilters appends the soft-delete, calendar, status, category, and
// date clauses shared by every journal query, in one fixed order.
func (w *whereBuilder) addJournalFilters(arg JournalFilterParams) {
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	if arg.Category != "" {
		w.add(journalCategoryExists, arg.Category)
	}
	if arg.HideCancelled {
		w.add("status != 'CANCELLED'")
	}
	if arg.FromDate != "" {
		w.add("(start_date IS NULL OR start_date >= ?)", arg.FromDate)
	}
	if arg.ToDate != "" {
		w.add("(start_date IS NULL OR start_date < ?)", arg.ToDate)
	}
}

func (q *Queries) ListJournalsFiltered(ctx context.Context, arg JournalFilterParams) ([]Journal, error) {
	var w whereBuilder
	// Non-recurring, non-RDATE-only masters (RDATE-only handled by ListRecurringJournalsFiltered).
	w.add("recurrence_rule IS NULL AND (rdates IS NULL OR rdates = '') AND recurrence_id = ''")
	w.addJournalFilters(arg)
	where, args := w.build()
	return q.queryJournals(ctx, where, args, "start_date ASC, summary ASC")
}

func (q *Queries) ListRecurringJournalsFiltered(ctx context.Context, arg JournalFilterParams) ([]Journal, error) {
	var w whereBuilder
	// RRULE masters and RDATE-only masters (no RRULE but has RDATEs); both need expansion.
	w.add("(recurrence_rule IS NOT NULL OR (rdates IS NOT NULL AND rdates != '')) AND recurrence_id = ''")
	w.addJournalFilters(arg)
	where, args := w.build()
	return q.queryJournals(ctx, where, args, "start_date ASC, summary ASC")
}

func (q *Queries) ListJournalsForExport(ctx context.Context, arg JournalFilterParams) ([]Journal, error) {
	var w whereBuilder
	w.addJournalFilters(arg)
	where, args := w.build()
	return q.queryJournals(ctx, where, args, "start_date ASC, summary ASC")
}
