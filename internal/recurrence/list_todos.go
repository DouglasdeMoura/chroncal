package recurrence

import (
	"context"
	"sort"
	"time"

	"github.com/douglasdemoura/chroncal/internal/recurset"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// todoOverridesByUID is the todo analogue of eventOverridesByUID.
func (s *Service) todoOverridesByUID(ctx context.Context, masters []storage.Todo) (map[string][]storage.Todo, error) {
	if len(masters) == 0 {
		return nil, nil
	}
	uids := make([]string, len(masters))
	for i, m := range masters {
		uids[i] = m.Uid
	}
	rows, err := s.q.ListTodoOverridesByUIDs(ctx, uids)
	if err != nil {
		return nil, err
	}
	byUID := make(map[string][]storage.Todo, len(rows))
	for _, r := range rows {
		key := seriesKey(r.CalendarID, r.Uid)
		byUID[key] = append(byUID[key], r)
	}
	return byUID, nil
}

func todoFromRow(row storage.Todo) todo.Todo {
	return todo.Todo{
		ID:              row.ID,
		UID:             row.Uid,
		CalendarID:      row.CalendarID,
		Summary:         row.Summary,
		Description:     storage.NullableToString(row.Description),
		Location:        storage.NullableToString(row.Location),
		DueDate:         storage.NullableToString(row.DueDate),
		StartDate:       storage.NullableToString(row.StartDate),
		Duration:        storage.NullableToString(row.Duration),
		CompletedAt:     storage.NullableToString(row.CompletedAt),
		PercentComplete: row.PercentComplete,
		Status:          row.Status,
		Priority:        row.Priority,
		Class:           row.Class,
		URL:             storage.NullableToString(row.Url),
		RecurrenceRule:  storage.NullableToString(row.RecurrenceRule),
		Timezone:        storage.NullableToString(row.Timezone),
		Sequence:        row.Sequence,
		ExDates:         storage.NullableToString(row.Exdates),
		RDates:          storage.NullableToString(row.Rdates),
		RecurrenceID:    row.RecurrenceID,
		Geo:             storage.NullableToString(row.Geo),
		DtStamp:         storage.NullableToString(row.Dtstamp),
		CreatedAt:       timeutil.ParseDateTime(row.CreatedAt),
		UpdatedAt:       timeutil.ParseDateTime(row.UpdatedAt),
		DeletedAt:       deletedAtOf(row.DeletedAt),
	}
}

// deletedAtOf parses the deleted_at column of a row. It returns nil for a
// live row, so a list with IncludeDeleted can mark the rows in the trash.
func deletedAtOf(deletedAt *string) *time.Time {
	if deletedAt == nil || *deletedAt == "" {
		return nil
	}
	t := timeutil.ParseDateTime(*deletedAt)
	return &t
}

func (s *Service) populateTodoCategories(ctx context.Context, todos []todo.Todo) {
	populateCategories(ctx, todos,
		func(t todo.Todo) int64 { return t.ID },
		s.q.ListCategoriesByTodoIDs,
		func(r storage.TodoCategory) (int64, string) { return r.TodoID, r.Category },
		func(t *todo.Todo, joined string) { t.Categories = joined },
	)
}

// todoAnchor returns a todo's recurrence anchor: DTSTART if present, else DUE.
func todoAnchor(td todo.Todo) time.Time {
	anchor := td.ParseStartDate()
	if anchor.IsZero() {
		anchor = td.ParseDueDate()
	}
	return anchor
}

// todoDuration is the START->DUE span used to keep an occurrence that spans the window. A
// due-only (point) todo has none.
func todoDuration(td todo.Todo) time.Duration {
	if start := td.ParseStartDate(); !start.IsZero() {
		if due := td.ParseDueDate(); !due.IsZero() && due.After(start) {
			return due.Sub(start)
		}
	}
	return 0
}

// newTodoRRuleSet parses td's RRULE into a reusable recurset.Set for
// expansion, with the EXDATEs applied. ok is false when the todo has no
// anchor (so it cannot be expanded).
func newTodoRRuleSet(td todo.Todo) (recurset.Set, bool) {
	anchor := todoAnchor(td)
	if anchor.IsZero() {
		return recurset.Set{}, false
	}
	return recurset.New(td.RecurrenceRule, td.Timezone, anchor,
		todoDuration(td), td.ParseExDates(), td.ParseRDates())
}

// newTodoOccChecker builds a reusable orphan-detection checker for a recurring
// todo master. A cancelled series matches nothing (see newEventOccChecker).
func newTodoOccChecker(td todo.Todo) recurset.Checker {
	anchor := todoAnchor(td)
	if anchor.IsZero() || cancelledRecurringMaster(td.RecurrenceRule, td.Status) {
		return recurset.Checker{}
	}
	return recurset.NewChecker(td.RecurrenceRule, td.Timezone, anchor, td.ParseRDates())
}

// singleTodoInstance returns td as a lone occurrence (non-recurring or
// unparseable RRULE) if its anchor falls within [from, to).
func singleTodoInstance(td todo.Todo, from, to time.Time) []ExpandedTodo {
	anchor := todoAnchor(td)
	if anchor.IsZero() || anchor.Before(from) || !anchor.Before(to) {
		return nil
	}
	return []ExpandedTodo{{Todo: td, InstanceTime: anchor}}
}

// ExpandTodo generates all occurrences of a todo within a date range.
// The anchor date is DTSTART if present, else DUE. For non-recurring todos
// a single instance is returned if the anchor falls in range.
func ExpandTodo(td todo.Todo, from, to time.Time) []ExpandedTodo {
	// A cancelled recurring master has no occurrences (see cancelledRecurringMaster).
	if cancelledRecurringMaster(td.RecurrenceRule, td.Status) {
		return nil
	}
	rs, ok := newTodoRRuleSet(td)
	if !ok {
		return singleTodoInstance(td, from, to)
	}

	var instances []ExpandedTodo
	for _, occ := range rs.Between(from, to) {
		_, isRDate := rs.RDates[recurset.RDateKey(occ)]
		instances = append(instances, ExpandedTodo{
			Todo:         td,
			InstanceTime: occ.UTC(),
			IsOverride:   isRDate,
		})
	}
	return instances
}

// TodoAtInstance returns the todo of an instance, with its DUE and DTSTART
// moved to the occurrence. Each value keeps its own form, so a date-only
// value stays date-only.
func TodoAtInstance(i ExpandedTodo) todo.Todo {
	t := i.Todo
	anchor := todoAnchor(t)
	if !anchor.IsZero() {
		offset := i.InstanceTime.Sub(anchor)
		t.DueDate = shiftDateString(t.DueDate, t.ParseDueDate(), offset)
		t.StartDate = shiftDateString(t.StartDate, t.ParseStartDate(), offset)
	}
	return t
}

// TodoOccurrence returns the occurrence of the recurring todo master at
// at, in the form of a new override: the dates of the occurrence, the
// RECURRENCE-ID of at, and no rule and no date lists (issue #887). ok is
// false when master does not recur or has no occurrence at at. EXDATEs
// apply.
func TodoOccurrence(master todo.Todo, at time.Time) (todo.Todo, bool) {
	want := at.UTC()
	rid := want.Format(time.RFC3339)
	// todo.OccursAt holds the rule, so the CLI and CreateOverride agree.
	if !todo.OccursAt(master, rid) {
		return todo.Todo{}, false
	}
	t := TodoAtInstance(ExpandedTodo{Todo: master, InstanceTime: want})
	t.ID = 0
	t.RecurrenceID = rid
	t.RecurrenceRule, t.ExDates, t.RDates = "", "", ""
	return t, true
}

// expandRecurringTodoRows expands recurring todo rows into Todo instances with
// DueDate/StartDate adjusted to each occurrence. For each master, overrides
// (rows with a match of RECURRENCE-ID) replace the original RRULE instance.
func (s *Service) expandRecurringTodoRows(ctx context.Context, rows []storage.Todo, from, to time.Time) ([]todo.Todo, error) {
	k := recurringKind[storage.Todo, todo.Todo, ExpandedTodo]{
		fromRow:  todoFromRow,
		expand:   ExpandTodo,
		instTime: func(i ExpandedTodo) time.Time { return i.InstanceTime },
		applyInstance: func(i ExpandedTodo) todo.Todo {
			t := TodoAtInstance(i)
			t.InstanceTime = i.InstanceTime
			return t
		},
		uid: func(r storage.Todo) string { return seriesKey(r.CalendarID, r.Uid) },
		dateOnly: func(r storage.Todo) bool {
			return todo.Todo{StartDate: storage.NullableToString(r.StartDate), DueDate: storage.NullableToString(r.DueDate)}.AnchorIsDateOnly()
		},
		status:         func(r storage.Todo) string { return r.Status },
		recurrenceID:   func(r storage.Todo) string { return r.RecurrenceID },
		overridesByUID: s.todoOverridesByUID,
		newOccChecker:  newTodoOccChecker,
		emitOverride: func(o storage.Todo, from, to time.Time) (todo.Todo, bool) {
			ot := todoFromRow(o)
			anchor := ot.ParseStartDate()
			if anchor.IsZero() {
				anchor = ot.ParseDueDate()
			}
			if anchor.IsZero() {
				// No datable anchor: fall back to the replaced slot for the
				// window check. An unparseable recurrence_id leaves anchor zero,
				// which fails recurset.KeepOccurrence and is dropped (the orphan probe
				// that follows would drop it too).
				anchor, _ = timeutil.ParseRecurrenceID(o.RecurrenceID)
			}
			// Keep on [START, DUE) interval overlap (honoring todoDuration), so a
			// multi-day override whose START precedes the window but whose DUE
			// falls inside it is not dropped -- matching the master-occurrence
			// path (recurset.Set.Between) and the event override path.
			return ot, recurset.KeepOccurrence(anchor, todoDuration(ot), from, to)
		},
	}
	return expandRecurringRowsBy(ctx, k, rows, from, to)
}

// ListExpandedTodosByDueDateRange returns non-recurring todos in [from,to)
// merged with expanded instances of recurring todo masters.
func (s *Service) ListExpandedTodosByDueDateRange(ctx context.Context, from, to time.Time) ([]todo.Todo, error) {
	fromStr := from.Format("2006-01-02")
	toStr := to.Format("2006-01-02")
	rangeRows, err := s.q.ListTodosByDueDateRange(ctx, storage.ListTodosByDueDateRangeParams{
		DueDate:   &fromStr,
		DueDate_2: &toStr,
	})
	if err != nil {
		return nil, err
	}
	// Keep only non-recurring, non-RDATE-only masters from the due-date results.
	var result []todo.Todo
	for _, row := range rangeRows {
		if row.RecurrenceRule == nil && row.RecurrenceID == "" && !isRDateOnlyMaster(row.Rdates) {
			result = append(result, todoFromRow(row))
		}
	}

	recurringRows, err := s.q.ListRecurringTodos(ctx)
	if err != nil {
		return nil, err
	}
	expanded, err := s.expandRecurringTodoRows(ctx, recurringRows, from, to)
	if err != nil {
		return nil, err
	}
	result = append(result, expanded...)

	s.populateTodoCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		di := result[i].LocalDue()
		dj := result[j].LocalDue()
		if di.IsZero() {
			return false
		}
		if dj.IsZero() {
			return true
		}
		return di.Before(dj)
	})
	return result, nil
}

// TodoListParams holds composable filters for todo lists.
type TodoListParams struct {
	CalendarID    int64
	Status        string
	HideCompleted bool
	From          time.Time
	To            time.Time
	// IncludeDeleted, when true, returns soft-deleted todos alongside live
	// rows. Default (false) hides them, matching the live-query contract
	// every other service method honors.
	IncludeDeleted bool
}

// ListFilteredTodos returns todos that match all supplied filters. When a date
// range is provided, recurring todos are expanded. Otherwise master entries
// are returned as-is.
func (s *Service) ListFilteredTodos(ctx context.Context, p TodoListParams) ([]todo.Todo, error) {
	fromStr := ""
	toStr := ""
	hasRange := !p.From.IsZero() || !p.To.IsZero()
	if !p.From.IsZero() {
		fromStr = p.From.Format("2006-01-02")
	}
	if !p.To.IsZero() {
		toStr = p.To.Format("2006-01-02")
	}

	rows, err := s.q.ListTodosFiltered(ctx, storage.TodoFilterParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		HideCompleted:  p.HideCompleted,
		FromDate:       fromStr,
		ToDate:         toStr,
		IncludeDeleted: p.IncludeDeleted,
	})
	if err != nil {
		return nil, err
	}

	var result []todo.Todo
	for _, row := range rows {
		result = append(result, todoFromRow(row))
	}

	recurringRows, err := s.q.ListRecurringTodosFiltered(ctx, storage.TodoFilterParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		HideCompleted:  p.HideCompleted,
		IncludeDeleted: p.IncludeDeleted,
	})
	if err != nil {
		return nil, err
	}
	if hasRange {
		expanded, err := s.expandRecurringTodoRows(ctx, recurringRows, p.From, p.To)
		if err != nil {
			return nil, err
		}
		result = append(result, expanded...)
	} else {
		for _, row := range recurringRows {
			result = append(result, todoFromRow(row))
		}
	}

	s.populateTodoCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		di := result[i].LocalDue()
		dj := result[j].LocalDue()
		if di.IsZero() {
			return false
		}
		if dj.IsZero() {
			return true
		}
		return di.Before(dj)
	})
	return result, nil
}
