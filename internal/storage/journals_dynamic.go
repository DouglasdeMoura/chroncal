package storage

import "context"

const journalCategoryExists = "EXISTS (SELECT 1 FROM journal_categories jc WHERE jc.journal_id = journals.id AND jc.category = ?)"

// addJournalListFilters appends the calendar / status / hide-cancelled clauses
// shared verbatim by ListJournalsFiltered and ListRecurringJournalsFiltered, in
// the same order so positional args line up.
func (w *whereBuilder) addJournalListFilters(calendarID int64, filterStatus string, hideCancelled int64) {
	if calendarID != 0 {
		w.add("calendar_id = ?", calendarID)
	}
	if filterStatus != "" {
		w.add("status = ?", filterStatus)
	}
	if hideCancelled != 0 {
		w.add("status != 'CANCELLED'")
	}
}

type ListJournalsFilteredParams struct {
	CalendarID     int64
	FilterStatus   string
	HideCancelled  int64
	FromDate       string
	ToDate         string
	IncludeDeleted bool
	DeletedOnly    bool
}

func (q *Queries) ListJournalsFiltered(ctx context.Context, arg ListJournalsFilteredParams) ([]Journal, error) {
	var w whereBuilder
	// Non-recurring, non-RDATE-only masters (RDATE-only handled by ListRecurringJournalsFiltered).
	w.add("recurrence_rule IS NULL AND (rdates IS NULL OR rdates = '') AND recurrence_id = ''")
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
	w.addJournalListFilters(arg.CalendarID, arg.FilterStatus, arg.HideCancelled)
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
	HideCancelled  int64
	IncludeDeleted bool
	DeletedOnly    bool
}

func (q *Queries) ListRecurringJournalsFiltered(ctx context.Context, arg ListRecurringJournalsFilteredParams) ([]Journal, error) {
	var w whereBuilder
	// RRULE masters and RDATE-only masters (no RRULE but has RDATEs); both need expansion.
	w.add("(recurrence_rule IS NOT NULL OR (rdates IS NOT NULL AND rdates != '')) AND recurrence_id = ''")
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
	w.addJournalListFilters(arg.CalendarID, arg.FilterStatus, arg.HideCancelled)
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
	w.addSoftDeleteFilter(arg.IncludeDeleted, arg.DeletedOnly)
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
