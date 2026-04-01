package recurrence

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// Service handles recurrence expansion and caching
type Service struct {
	db *sql.DB
	q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
}

// tzForExpansion returns the *time.Location to use for rrule expansion.
// If tz is a valid IANA timezone, expansion happens in that timezone so
// wall-clock times are preserved across DST transitions. Otherwise nil
// is returned and expansion happens in whatever timezone the times carry.
func tzForExpansion(tz string) *time.Location {
	if tz == "" {
		return nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil
	}
	// Don't bother converting for fixed-offset zones (no DST to handle).
	if loc == time.UTC {
		return nil
	}
	return loc
}

// ExpandEvent generates all occurrences of an event within a date range
// Returns instances even for non-recurring events (single instance)
func ExpandEvent(evt event.Event, from, to time.Time) []ExpandedEvent {
	if evt.RecurrenceRule == "" {
		// Non-recurring event - return single instance if in range
		if evt.StartTime.Before(from) || !evt.StartTime.Before(to) {
			return nil
		}
		return []ExpandedEvent{{
			Event:        evt,
			InstanceTime: evt.StartTime,
			IsOverride:   false,
		}}
	}

	// Parse RRULE
	rruleStr := "RRULE:" + evt.RecurrenceRule
	set, err := rrule.StrToRRuleSet(rruleStr)
	if err != nil {
		// Invalid RRULE - fall back to single instance
		if evt.StartTime.Before(from) || !evt.StartTime.Before(to) {
			return nil
		}
		return []ExpandedEvent{{
			Event:        evt,
			InstanceTime: evt.StartTime,
			IsOverride:   false,
		}}
	}

	// If the event has a named timezone, expand in that timezone so
	// wall-clock times are preserved across DST boundaries.
	loc := tzForExpansion(evt.Timezone)
	dtstart := evt.StartTime
	localFrom, localTo := from, to
	if loc != nil {
		dtstart = dtstart.In(loc)
		localFrom = from.In(loc)
		localTo = to.In(loc)
	}

	set.DTStart(dtstart)

	for _, ex := range evt.ParseExDates() {
		if loc != nil {
			ex = ex.In(loc)
		}
		set.ExDate(ex)
	}

	rdates := evt.ParseRDates()
	for _, rd := range rdates {
		if loc != nil {
			rd = rd.In(loc)
		}
		set.RDate(rd)
	}

	// Build a set of RDATE times for O(1) membership checks below.
	rdateSet := make(map[time.Time]struct{}, len(rdates))
	for _, rd := range rdates {
		if loc != nil {
			rd = rd.In(loc)
		}
		rdateSet[rd] = struct{}{}
	}

	// Get all occurrences in range [from, to)
	occurrences := set.Between(localFrom, localTo, true)
	// Enforce half-open upper bound: exclude occurrences exactly at 'to'.
	filtered := occurrences[:0]
	for _, occ := range occurrences {
		if occ.Before(localTo) {
			filtered = append(filtered, occ)
		}
	}
	occurrences = filtered

	var instances []ExpandedEvent
	for _, occ := range occurrences {
		utcOcc := occ.UTC()
		_, isRDate := rdateSet[occ]

		instances = append(instances, ExpandedEvent{
			Event:        evt,
			InstanceTime: utcOcc,
			IsOverride:   isRDate,
		})
	}

	return instances
}

// ExpandOption configures ListExpandedEvents behaviour.
type ExpandOption func(*expandOptions)

type expandOptions struct {
	skipCategories bool
}

// SkipCategories omits the batch category load. Use this when the caller
// does not need Event.Categories (e.g. alarm checking).
func SkipCategories() ExpandOption {
	return func(o *expandOptions) { o.skipCategories = true }
}

// ListExpandedEvents returns events with their instances in a date range.
// Uses filtered queries instead of loading the entire table.
func (s *Service) ListExpandedEvents(ctx context.Context, from, to time.Time, opts ...ExpandOption) ([]ExpandedEvent, error) {
	var o expandOptions
	for _, fn := range opts {
		fn(&o)
	}
	// Non-recurring events in date range.
	rangeRows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime: to.Format(time.RFC3339),   // start_time < to
		EndTime:   from.Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}

	// All recurring master events (need full set for expansion).
	recurRows, err := s.q.ListRecurringEvents(ctx)
	if err != nil {
		return nil, err
	}

	var results []ExpandedEvent

	for _, row := range rangeRows {
		// Skip recurring masters (handled below) but keep non-recurring events.
		if row.RecurrenceRule != nil && row.RecurrenceID == "" {
			continue
		}
		// Skip overrides; they're merged during recurring expansion below.
		if row.RecurrenceID != "" {
			continue
		}
		evt := eventFromRow(row)
		results = append(results, ExpandedEvent{
			Event:        evt,
			InstanceTime: evt.StartTime,
		})
	}

	for _, row := range recurRows {
		evt := eventFromRow(row)
		expanded := ExpandEvent(evt, from, to)

		// Fetch overrides for this master and substitute them.
		overrides, _ := s.q.ListOverridesByUID(ctx, row.Uid)
		overrideMap := make(map[string]storage.Event, len(overrides))
		for _, o := range overrides {
			overrideMap[o.RecurrenceID] = o
		}

		for _, inst := range expanded {
			instKey := inst.InstanceTime.UTC().Format(time.RFC3339)
			if o, ok := overrideMap[instKey]; ok {
				if strings.EqualFold(o.Status, "CANCELLED") {
					continue
				}
				oe := eventFromRow(o)
				results = append(results, ExpandedEvent{
					Event:        oe,
					InstanceTime: oe.StartTime,
				})
			} else {
				results = append(results, inst)
			}
		}
	}

	if !o.skipCategories && len(results) > 0 {
		ids := make([]int64, len(results))
		for i := range results {
			ids[i] = results[i].Event.ID
		}
		cats, err := s.q.ListCategoriesByEventIDs(ctx, ids)
		if err == nil {
			catMap := make(map[int64][]string, len(results))
			for _, c := range cats {
				catMap[c.EventID] = append(catMap[c.EventID], c.Category)
			}
			for i := range results {
				if c, ok := catMap[results[i].Event.ID]; ok {
					results[i].Event.Categories = strings.Join(c, ",")
				}
			}
		}
	}

	return results, nil
}


func eventFromRow(row storage.Event) event.Event {
	return event.Event{
		ID:             row.ID,
		UID:            row.Uid,
		CalendarID:     row.CalendarID,
		Title:          row.Title,
		Description:    storage.NullableToString(row.Description),
		Location:       storage.NullableToString(row.Location),
		StartTime:      timeutil.ParseDateTime(row.StartTime),
		EndTime:        timeutil.ParseDateTime(row.EndTime),
		AllDay:         row.AllDay != 0,
		RecurrenceRule: storage.NullableToString(row.RecurrenceRule),
		Timezone:       storage.NullableToString(row.Timezone),
		Status:         row.Status,
		Transp:         row.Transp,
		Sequence:       row.Sequence,
		Priority:       row.Priority,
		Class:          row.Class,
		URL:            storage.NullableToString(row.Url),
		ExDates:        storage.NullableToString(row.Exdates),
		RDates:         storage.NullableToString(row.Rdates),
		RecurrenceID:   row.RecurrenceID,
		Geo:            storage.NullableToString(row.Geo),
		DurationValue:  storage.NullableToString(row.Duration),
		DtStamp:        storage.NullableToString(row.Dtstamp),
		CreatedAt:      timeutil.ParseDateTime(row.CreatedAt),
		UpdatedAt:      timeutil.ParseDateTime(row.UpdatedAt),
	}
}

// expandRecurringRows expands recurring event rows into Event instances with
// StartTime/EndTime adjusted to each occurrence. For each master, overrides
// (rows with a matching RECURRENCE-ID) replace the original RRULE instance.
func (s *Service) expandRecurringRows(ctx context.Context, rows []storage.Event, from, to time.Time) []event.Event {
	var result []event.Event
	for _, row := range rows {
		evt := eventFromRow(row)
		expanded := ExpandEvent(evt, from, to)

		// Fetch overrides for this master.
		overrides, _ := s.q.ListOverridesByUID(ctx, row.Uid)
		overrideMap := make(map[string]storage.Event, len(overrides))
		for _, o := range overrides {
			overrideMap[o.RecurrenceID] = o
		}

		for _, inst := range expanded {
			instKey := inst.InstanceTime.UTC().Format(time.RFC3339)
			if o, ok := overrideMap[instKey]; ok {
				// Override replaces this instance.
				if strings.EqualFold(o.Status, "CANCELLED") {
					continue // CANCELLED override suppresses the instance.
				}
				oe := eventFromRow(o)
				result = append(result, oe)
			} else {
				e := inst.Event
				dur := e.EndTime.Sub(e.StartTime)
				e.StartTime = inst.InstanceTime
				e.EndTime = inst.InstanceTime.Add(dur)
				result = append(result, e)
			}
		}
	}
	return result
}

// ListExpandedByDateRange returns non-recurring events in [from,to) merged
// with expanded instances of recurring event masters. The returned events have
// StartTime/EndTime adjusted to the instance time and are sorted by StartTime.
func (s *Service) ListExpandedByDateRange(ctx context.Context, from, to time.Time) ([]event.Event, error) {
	rangeRows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime: to.Format(time.RFC3339),   // start_time < to
		EndTime:   from.Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}
	// Keep only non-recurring from the date-range results.
	var result []event.Event
	for _, row := range rangeRows {
		if row.RecurrenceRule == nil && row.RecurrenceID == "" {
			result = append(result, eventFromRow(row))
		}
	}

	recurringRows, err := s.q.ListRecurringEvents(ctx)
	if err != nil {
		return nil, err
	}
	result = append(result, s.expandRecurringRows(ctx, recurringRows, from, to)...)

	s.populateEventCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result, nil
}

// ExportFilterParams holds filters for ICS export of recurring masters.
type ExportFilterParams struct {
	CalendarID int64
	Category   string
	Status     string
	From       time.Time
	To         time.Time
}

// ExportExpandedByDateRange returns recurring event masters (not expanded
// instances) that have at least one occurrence in [from,to), merged with
// non-recurring events whose start_time is in range. This is for ICS export
// where the master VEVENT with RRULE should be emitted, not individual
// instances. All filters (calendar, category, status) are applied at the
// SQL level.
func (s *Service) ExportExpandedByDateRange(ctx context.Context, p ExportFilterParams) ([]event.Event, error) {
	fromStr := ""
	toStr := ""
	if !p.From.IsZero() {
		fromStr = p.From.Format(time.RFC3339)
	}
	if !p.To.IsZero() {
		toStr = p.To.Format(time.RFC3339)
	}

	rangeRows, err := s.q.ListEventsForExport(ctx, storage.EventFilterParams{
		CalendarID:   p.CalendarID,
		FromTime:     fromStr,
		ToTime:       toStr,
		Category:     p.Category,
		FilterStatus: p.Status,
	})
	if err != nil {
		return nil, err
	}
	var result []event.Event
	seen := make(map[int64]bool)
	for _, row := range rangeRows {
		if row.RecurrenceRule == nil {
			result = append(result, eventFromRow(row))
			seen[row.ID] = true
		}
	}

	recurringRows, err := s.q.ListRecurringEventsFiltered(ctx, storage.EventFilterParams{
		CalendarID:   p.CalendarID,
		FilterStatus: p.Status,
		Category:     p.Category,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range recurringRows {
		if seen[row.ID] {
			continue
		}
		evt := eventFromRow(row)
		if len(ExpandEvent(evt, p.From, p.To)) > 0 {
			result = append(result, evt)
		}
	}

	s.populateEventCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result, nil
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
	}
}

func (s *Service) populateEventCategories(ctx context.Context, events []event.Event) {
	if len(events) == 0 {
		return
	}
	ids := make([]int64, len(events))
	for i := range events {
		ids[i] = events[i].ID
	}
	rows, err := s.q.ListCategoriesByEventIDs(ctx, ids)
	if err != nil {
		return
	}
	catMap := make(map[int64][]string, len(events))
	for _, r := range rows {
		catMap[r.EventID] = append(catMap[r.EventID], r.Category)
	}
	for i := range events {
		if cats, ok := catMap[events[i].ID]; ok {
			events[i].Categories = strings.Join(cats, ",")
		}
	}
}

func (s *Service) populateTodoCategories(ctx context.Context, todos []todo.Todo) {
	if len(todos) == 0 {
		return
	}
	ids := make([]int64, len(todos))
	for i := range todos {
		ids[i] = todos[i].ID
	}
	rows, err := s.q.ListCategoriesByTodoIDs(ctx, ids)
	if err != nil {
		return
	}
	catMap := make(map[int64][]string, len(todos))
	for _, r := range rows {
		catMap[r.TodoID] = append(catMap[r.TodoID], r.Category)
	}
	for i := range todos {
		if cats, ok := catMap[todos[i].ID]; ok {
			todos[i].Categories = strings.Join(cats, ",")
		}
	}
}

// expandRecurringTodoRows expands recurring todo rows into Todo instances with
// DueDate/StartDate adjusted to each occurrence. For each master, overrides
// (rows with a matching RECURRENCE-ID) replace the original RRULE instance.
func (s *Service) expandRecurringTodoRows(ctx context.Context, rows []storage.Todo, from, to time.Time) []todo.Todo {
	var result []todo.Todo
	for _, row := range rows {
		td := todoFromRow(row)
		expanded := ExpandTodo(td, from, to)

		// Fetch overrides for this master.
		overrides, _ := s.q.ListTodoOverridesByUID(ctx, row.Uid)
		overrideMap := make(map[string]storage.Todo, len(overrides))
		for _, o := range overrides {
			overrideMap[o.RecurrenceID] = o
		}

		for _, inst := range expanded {
			instKey := inst.InstanceTime.UTC().Format(time.RFC3339)
			if o, ok := overrideMap[instKey]; ok {
				if strings.EqualFold(o.Status, "CANCELLED") {
					continue // CANCELLED override suppresses the instance.
				}
				ot := todoFromRow(o)
				result = append(result, ot)
			} else {
				t := inst.Todo
				anchor := t.ParseStartDate()
				if anchor.IsZero() {
					anchor = t.ParseDueDate()
				}
				if !anchor.IsZero() {
					offset := inst.InstanceTime.Sub(anchor)
					if t.DueDate != "" {
						due := t.ParseDueDate()
						if !due.IsZero() {
							newDue := due.Add(offset)
							if timeutil.IsDateOnly(t.DueDate) {
								t.DueDate = newDue.Format("2006-01-02")
							} else {
								t.DueDate = newDue.Format(time.RFC3339)
							}
						}
					}
					if t.StartDate != "" {
						start := t.ParseStartDate()
						if !start.IsZero() {
							newStart := start.Add(offset)
							if timeutil.IsDateOnly(t.StartDate) {
								t.StartDate = newStart.Format("2006-01-02")
							} else {
								t.StartDate = newStart.Format(time.RFC3339)
							}
						}
					}
				}
				result = append(result, t)
			}
		}
	}
	return result
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
	var result []todo.Todo
	for _, row := range rangeRows {
		if row.RecurrenceRule == nil && row.RecurrenceID == "" {
			result = append(result, todoFromRow(row))
		}
	}

	recurringRows, err := s.q.ListRecurringTodos(ctx)
	if err != nil {
		return nil, err
	}
	result = append(result, s.expandRecurringTodoRows(ctx, recurringRows, from, to)...)

	s.populateTodoCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		di := result[i].ParseDueDate()
		dj := result[j].ParseDueDate()
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

// EventListParams holds composable filters for listing events.
type EventListParams struct {
	CalendarID int64
	Status     string
	Category   string
	From       time.Time
	To         time.Time
}

// ListFilteredEvents returns events matching all supplied filters. Calendar,
// status, and date-range filters compose freely. Recurring events are always
// expanded within the date range, with overrides applied.
func (s *Service) ListFilteredEvents(ctx context.Context, p EventListParams) ([]event.Event, error) {
	fromStr := ""
	toStr := ""
	if !p.From.IsZero() {
		fromStr = p.From.Format(time.RFC3339)
	}
	if !p.To.IsZero() {
		toStr = p.To.Format(time.RFC3339)
	}

	rangeRows, err := s.q.ListEventsFiltered(ctx, storage.EventFilterParams{
		CalendarID:   p.CalendarID,
		FilterStatus: p.Status,
		Category:     p.Category,
		FromTime:     fromStr,
		ToTime:       toStr,
	})
	if err != nil {
		return nil, err
	}

	var result []event.Event
	for _, row := range rangeRows {
		result = append(result, eventFromRow(row))
	}

	recurringRows, err := s.q.ListRecurringEventsFiltered(ctx, storage.EventFilterParams{
		CalendarID:   p.CalendarID,
		FilterStatus: p.Status,
		Category:     p.Category,
	})
	if err != nil {
		return nil, err
	}
	result = append(result, s.expandRecurringRows(ctx, recurringRows, p.From, p.To)...)

	s.populateEventCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result, nil
}

// TodoListParams holds composable filters for listing todos.
type TodoListParams struct {
	CalendarID    int64
	Status        string
	HideCompleted bool
	From          time.Time
	To            time.Time
}

// ListFilteredTodos returns todos matching all supplied filters. When a date
// range is provided, recurring todos are expanded; otherwise master entries
// are returned as-is.
func (s *Service) ListFilteredTodos(ctx context.Context, p TodoListParams) ([]todo.Todo, error) {
	hideCompleted := int64(0)
	if p.HideCompleted {
		hideCompleted = 1
	}

	fromStr := ""
	toStr := ""
	hasRange := !p.From.IsZero() || !p.To.IsZero()
	if !p.From.IsZero() {
		fromStr = p.From.Format("2006-01-02")
	}
	if !p.To.IsZero() {
		toStr = p.To.Format("2006-01-02")
	}

	rows, err := s.q.ListTodosFiltered(ctx, storage.ListTodosFilteredParams{
		CalendarID:    p.CalendarID,
		FilterStatus:  p.Status,
		HideCompleted: hideCompleted,
		FromDate:      fromStr,
		ToDate:        toStr,
	})
	if err != nil {
		return nil, err
	}

	var result []todo.Todo
	for _, row := range rows {
		result = append(result, todoFromRow(row))
	}

	recurringRows, err := s.q.ListRecurringTodosFiltered(ctx, storage.ListRecurringTodosFilteredParams{
		CalendarID:    p.CalendarID,
		FilterStatus:  p.Status,
		HideCompleted: hideCompleted,
	})
	if err != nil {
		return nil, err
	}
	if hasRange {
		result = append(result, s.expandRecurringTodoRows(ctx, recurringRows, p.From, p.To)...)
	} else {
		for _, row := range recurringRows {
			result = append(result, todoFromRow(row))
		}
	}

	s.populateTodoCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		di := result[i].ParseDueDate()
		dj := result[j].ParseDueDate()
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

// ExpandTodo generates all occurrences of a todo within a date range.
// The anchor date is DTSTART if present, else DUE. For non-recurring todos
// a single instance is returned if the anchor falls in range.
func ExpandTodo(td todo.Todo, from, to time.Time) []ExpandedTodo {
	anchor := td.ParseStartDate()
	if anchor.IsZero() {
		anchor = td.ParseDueDate()
	}
	if anchor.IsZero() {
		return nil
	}

	if td.RecurrenceRule == "" {
		if anchor.Before(from) || !anchor.Before(to) {
			return nil
		}
		return []ExpandedTodo{{
			Todo:         td,
			InstanceTime: anchor,
		}}
	}

	rruleStr := "RRULE:" + td.RecurrenceRule
	set, err := rrule.StrToRRuleSet(rruleStr)
	if err != nil {
		if anchor.Before(from) || !anchor.Before(to) {
			return nil
		}
		return []ExpandedTodo{{
			Todo:         td,
			InstanceTime: anchor,
		}}
	}

	loc := tzForExpansion(td.Timezone)
	dtstart := anchor
	localFrom, localTo := from, to
	if loc != nil {
		dtstart = dtstart.In(loc)
		localFrom = from.In(loc)
		localTo = to.In(loc)
	}

	set.DTStart(dtstart)
	for _, ex := range td.ParseExDates() {
		if loc != nil {
			ex = ex.In(loc)
		}
		set.ExDate(ex)
	}
	rdates := td.ParseRDates()
	for _, rd := range rdates {
		if loc != nil {
			rd = rd.In(loc)
		}
		set.RDate(rd)
	}

	rdateSet := make(map[time.Time]struct{}, len(rdates))
	for _, rd := range rdates {
		if loc != nil {
			rd = rd.In(loc)
		}
		rdateSet[rd] = struct{}{}
	}

	occurrences := set.Between(localFrom, localTo, true)
	filteredOcc := occurrences[:0]
	for _, occ := range occurrences {
		if occ.Before(localTo) {
			filteredOcc = append(filteredOcc, occ)
		}
	}
	occurrences = filteredOcc

	var instances []ExpandedTodo
	for _, occ := range occurrences {
		utcOcc := occ.UTC()
		_, isRDate := rdateSet[occ]
		instances = append(instances, ExpandedTodo{
			Todo:         td,
			InstanceTime: utcOcc,
			IsOverride:   isRDate,
		})
	}
	return instances
}
