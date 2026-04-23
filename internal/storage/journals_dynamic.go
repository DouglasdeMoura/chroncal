package storage

import "context"

const journalCategoryExists = "EXISTS (SELECT 1 FROM journal_categories jc WHERE jc.journal_id = journals.id AND jc.category = ?)"

// journalDeletedFilter mirrors todoDeletedFilter / event equivalents so the
// journals read path hides soft-deleted rows by default and exposes an
// explicit opt-in for trash views.
func journalDeletedFilter(w *whereBuilder, includeDeleted, deletedOnly bool) {
	switch {
	case deletedOnly:
		w.add("deleted_at IS NOT NULL")
	case !includeDeleted:
		w.add("deleted_at IS NULL")
	}
}

type ListJournalsFilteredParams struct {
	CalendarID     int64
	FilterStatus   string
	HideDrafts     int64
	FromDate       string
	ToDate         string
	IncludeDeleted bool
	DeletedOnly    bool
}

func (q *Queries) ListJournalsFiltered(ctx context.Context, arg ListJournalsFilteredParams) ([]Journal, error) {
	var w whereBuilder
	w.add("recurrence_rule IS NULL AND recurrence_id = ''")
	journalDeletedFilter(&w, arg.IncludeDeleted, arg.DeletedOnly)
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	if arg.HideDrafts != 0 {
		w.add("status != 'CANCELLED'")
	}
	if arg.FromDate != "" {
		w.add("(start_date IS NULL OR start_date >= ?)", arg.FromDate)
	}
	if arg.ToDate != "" {
		w.add("(start_date IS NULL OR start_date < ?)", arg.ToDate)
	}
	where, args := w.build()
	return q.queryJournals(ctx, where, args, "start_date ASC, summary ASC")
}

type ListRecurringJournalsFilteredParams struct {
	CalendarID     int64
	FilterStatus   string
	IncludeDeleted bool
	DeletedOnly    bool
}

func (q *Queries) ListRecurringJournalsFiltered(ctx context.Context, arg ListRecurringJournalsFilteredParams) ([]Journal, error) {
	var w whereBuilder
	w.add("recurrence_rule IS NOT NULL AND recurrence_id = ''")
	journalDeletedFilter(&w, arg.IncludeDeleted, arg.DeletedOnly)
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	where, args := w.build()
	return q.queryJournals(ctx, where, args, "start_date ASC, summary ASC")
}

type ListJournalsForExportParams struct {
	CalendarID     int64
	Category       string
	FilterStatus   string
	IncludeDeleted bool
	DeletedOnly    bool
}

func (q *Queries) ListJournalsForExport(ctx context.Context, arg ListJournalsForExportParams) ([]Journal, error) {
	var w whereBuilder
	journalDeletedFilter(&w, arg.IncludeDeleted, arg.DeletedOnly)
	if arg.CalendarID != 0 {
		w.add("calendar_id = ?", arg.CalendarID)
	}
	if arg.Category != "" {
		w.add(journalCategoryExists, arg.Category)
	}
	if arg.FilterStatus != "" {
		w.add("status = ?", arg.FilterStatus)
	}
	where, args := w.build()
	return q.queryJournals(ctx, where, args, "start_date ASC, summary ASC")
}
