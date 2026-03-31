package recurrence

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/storage"
	"github.com/douglasdemoura/tcal/internal/todo"
)

// Service handles recurrence expansion and caching
type Service struct {
	db *sql.DB
	q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
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

	// Set DTSTART to the event's start time
	set.DTStart(evt.StartTime)

	// Add EXDATEs
	for _, ex := range evt.ParseExDates() {
		set.ExDate(ex)
	}

	// Add RDATEs
	for _, rd := range evt.ParseRDates() {
		set.RDate(rd)
	}

	// Get all occurrences in range [from, to)
	occurrences := set.Between(from, to, true)
	// Enforce half-open upper bound: exclude occurrences exactly at 'to'.
	filtered := occurrences[:0]
	for _, occ := range occurrences {
		if occ.Before(to) {
			filtered = append(filtered, occ)
		}
	}
	occurrences = filtered

	var instances []ExpandedEvent
	for _, occ := range occurrences {
		// Check if this occurrence is from RDATE (override) or RRULE
		isRDate := false
		for _, rd := range evt.ParseRDates() {
			if occ.Equal(rd) {
				isRDate = true
				break
			}
		}

		instances = append(instances, ExpandedEvent{
			Event:        evt,
			InstanceTime: occ,
			IsOverride:   isRDate,
		})
	}

	return instances
}

// ExpandAndCache generates and caches instances for a single event
func (s *Service) ExpandAndCache(ctx context.Context, evt event.Event, from, to time.Time) error {
	if evt.RecurrenceRule == "" {
		return nil // Nothing to cache for non-recurring
	}

	instances := ExpandEvent(evt, from, to)

	// Use transaction for atomicity
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)

	// Clear existing instances in this range
	if err := q.DeleteRecurrenceInstances(ctx, storage.DeleteRecurrenceInstancesParams{
		EventID:    evt.ID,
		InstanceAt: from.Format(time.RFC3339),
	}); err != nil {
		return err
	}

	// Insert new instances
	for _, inst := range instances {
		_, err := q.InsertRecurrenceInstance(ctx, storage.InsertRecurrenceInstanceParams{
			EventID:    evt.ID,
			OriginalID: evt.ID,
			InstanceAt: inst.InstanceTime.Format(time.RFC3339),
			IsOverride: 0,
		})
		if err != nil && !isDuplicateError(err) {
			return err
		}
	}

	return tx.Commit()
}

// isDuplicateError checks if error is a unique constraint violation
func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ListExpandedEvents returns events with their instances in a date range
// This merges both recurring and non-recurring events
func (s *Service) ListExpandedEvents(ctx context.Context, from, to time.Time) ([]ExpandedEvent, error) {
	// Get all events that might have occurrences in this range
	// For recurring events, we need the parent event regardless of its start time
	// For non-recurring, they must be in range
	rows, err := s.q.ListAllEvents(ctx)
	if err != nil {
		return nil, err
	}

	var results []ExpandedEvent

	for _, row := range rows {
		evt := event.Event{
			ID:             row.ID,
			UID:            row.Uid,
			CalendarID:     row.CalendarID,
			Title:          row.Title,
			Description:    row.Description,
			Location:       row.Location,
			StartTime:      parseTime(row.StartTime),
			EndTime:        parseTime(row.EndTime),
			AllDay:         row.AllDay != 0,
			RecurrenceRule: row.RecurrenceRule,
			Timezone:       row.Timezone,
			Status:         row.Status,
			Transp:         row.Transp,
			Sequence:       row.Sequence,
			Priority:       row.Priority,
			Class:          row.Class,
			URL:            row.Url,
			Categories:     row.Categories,
			ExDates:        row.Exdates,
			RDates:         row.Rdates,
			RecurrenceID:   row.RecurrenceID,
			Geo:            row.Geo,
			CreatedAt:      parseTime(row.CreatedAt),
			UpdatedAt:      parseTime(row.UpdatedAt),
		}

		instances := ExpandEvent(evt, from, to)
		results = append(results, instances...)
	}

	return results, nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func eventFromRow(row storage.EventsV) event.Event {
	return event.Event{
		ID:             row.ID,
		UID:            row.Uid,
		CalendarID:     row.CalendarID,
		Title:          row.Title,
		Description:    row.Description,
		Location:       row.Location,
		StartTime:      parseTime(row.StartTime),
		EndTime:        parseTime(row.EndTime),
		AllDay:         row.AllDay != 0,
		RecurrenceRule: row.RecurrenceRule,
		Timezone:       row.Timezone,
		Status:         row.Status,
		Transp:         row.Transp,
		Sequence:       row.Sequence,
		Priority:       row.Priority,
		Class:          row.Class,
		URL:            row.Url,
		Categories:     row.Categories,
		ExDates:        row.Exdates,
		RDates:         row.Rdates,
		RecurrenceID:   row.RecurrenceID,
		Geo:            row.Geo,
		CreatedAt:      parseTime(row.CreatedAt),
		UpdatedAt:      parseTime(row.UpdatedAt),
	}
}

// expandRecurringRows expands recurring event rows into Event instances with
// StartTime/EndTime adjusted to each occurrence. For each master, overrides
// (rows with a matching RECURRENCE-ID) replace the original RRULE instance.
func (s *Service) expandRecurringRows(ctx context.Context, rows []storage.EventsV, from, to time.Time) []event.Event {
	var result []event.Event
	for _, row := range rows {
		evt := eventFromRow(row)
		expanded := ExpandEvent(evt, from, to)

		// Fetch overrides for this master.
		overrides, _ := s.q.ListOverridesByUID(ctx, row.Uid)
		overrideMap := make(map[string]storage.EventsV, len(overrides))
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
		StartTime:   from.Format(time.RFC3339),
		StartTime_2: to.Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	// Keep only non-recurring from the date-range results.
	var result []event.Event
	for _, row := range rangeRows {
		if row.RecurrenceRule == "" && row.RecurrenceID == "" {
			result = append(result, eventFromRow(row))
		}
	}

	recurringRows, err := s.q.ListRecurringEvents(ctx)
	if err != nil {
		return nil, err
	}
	result = append(result, s.expandRecurringRows(ctx, recurringRows, from, to)...)

	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result, nil
}

// ListExpandedByCalendarAndDateRange is like ListExpandedByDateRange but
// scoped to a single calendar.
func (s *Service) ListExpandedByCalendarAndDateRange(ctx context.Context, calID int64, from, to time.Time) ([]event.Event, error) {
	rangeRows, err := s.q.ListEventsByCalendarAndDateRange(ctx, storage.ListEventsByCalendarAndDateRangeParams{
		CalendarID:  calID,
		StartTime:   from.Format(time.RFC3339),
		StartTime_2: to.Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	var result []event.Event
	for _, row := range rangeRows {
		if row.RecurrenceRule == "" && row.RecurrenceID == "" {
			result = append(result, eventFromRow(row))
		}
	}

	recurringRows, err := s.q.ListRecurringEventsByCalendar(ctx, calID)
	if err != nil {
		return nil, err
	}
	result = append(result, s.expandRecurringRows(ctx, recurringRows, from, to)...)

	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result, nil
}

// ListExpandedByStatusAndDateRange is like ListExpandedByDateRange but
// filtered by event status.
func (s *Service) ListExpandedByStatusAndDateRange(ctx context.Context, status string, from, to time.Time) ([]event.Event, error) {
	rangeRows, err := s.q.ListEventsByStatusAndDateRange(ctx, storage.ListEventsByStatusAndDateRangeParams{
		Status:      status,
		StartTime:   from.Format(time.RFC3339),
		StartTime_2: to.Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	var result []event.Event
	for _, row := range rangeRows {
		if row.RecurrenceRule == "" && row.RecurrenceID == "" {
			result = append(result, eventFromRow(row))
		}
	}

	recurringRows, err := s.q.ListRecurringEventsByStatus(ctx, status)
	if err != nil {
		return nil, err
	}
	result = append(result, s.expandRecurringRows(ctx, recurringRows, from, to)...)

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

	rangeRows, err := s.q.ListEventsForExport(ctx, storage.ListEventsForExportParams{
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
		if row.RecurrenceRule == "" {
			result = append(result, eventFromRow(row))
			seen[row.ID] = true
		}
	}

	recurringRows, err := s.q.ListRecurringEventsFiltered(ctx, storage.ListRecurringEventsFilteredParams{
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

	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result, nil
}

func todoFromRow(row storage.TodosV) todo.Todo {
	return todo.Todo{
		ID:              row.ID,
		UID:             row.Uid,
		CalendarID:      row.CalendarID,
		Summary:         row.Summary,
		Description:     row.Description,
		Location:        row.Location,
		DueDate:         row.DueDate,
		StartDate:       row.StartDate,
		Duration:        row.Duration,
		CompletedAt:     row.CompletedAt,
		PercentComplete: row.PercentComplete,
		Status:          row.Status,
		Priority:        row.Priority,
		Class:           row.Class,
		URL:             row.Url,
		Categories:      row.Categories,
		RecurrenceRule:  row.RecurrenceRule,
		Timezone:        row.Timezone,
		Sequence:        row.Sequence,
		ExDates:         row.Exdates,
		RDates:          row.Rdates,
		RecurrenceID:    row.RecurrenceID,
		Geo:             row.Geo,
		CreatedAt:       parseTime(row.CreatedAt),
		UpdatedAt:       parseTime(row.UpdatedAt),
	}
}

// expandRecurringTodoRows expands recurring todo rows into Todo instances with
// DueDate/StartDate adjusted to each occurrence.
func expandRecurringTodoRows(rows []storage.TodosV, from, to time.Time) []todo.Todo {
	var result []todo.Todo
	for _, row := range rows {
		td := todoFromRow(row)
		for _, inst := range ExpandTodo(td, from, to) {
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
						if isDateOnly(t.DueDate) {
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
						if isDateOnly(t.StartDate) {
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
	return result
}

func isDateOnly(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ListExpandedTodosByDueDateRange returns non-recurring todos in [from,to)
// merged with expanded instances of recurring todo masters.
func (s *Service) ListExpandedTodosByDueDateRange(ctx context.Context, from, to time.Time) ([]todo.Todo, error) {
	rangeRows, err := s.q.ListTodosByDueDateRange(ctx, storage.ListTodosByDueDateRangeParams{
		DueDate:   from.Format("2006-01-02"),
		DueDate_2: to.Format("2006-01-02"),
	})
	if err != nil {
		return nil, err
	}
	var result []todo.Todo
	for _, row := range rangeRows {
		if row.RecurrenceRule == "" && row.RecurrenceID == "" {
			result = append(result, todoFromRow(row))
		}
	}

	recurringRows, err := s.q.ListRecurringTodos(ctx)
	if err != nil {
		return nil, err
	}
	result = append(result, expandRecurringTodoRows(recurringRows, from, to)...)

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

	rangeRows, err := s.q.ListEventsFiltered(ctx, storage.ListEventsFilteredParams{
		CalendarID:   p.CalendarID,
		FilterStatus: p.Status,
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

	recurringRows, err := s.q.ListRecurringEventsFiltered(ctx, storage.ListRecurringEventsFilteredParams{
		CalendarID:   p.CalendarID,
		FilterStatus: p.Status,
		Category:     "",
	})
	if err != nil {
		return nil, err
	}
	result = append(result, s.expandRecurringRows(ctx, recurringRows, p.From, p.To)...)

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
		result = append(result, expandRecurringTodoRows(recurringRows, p.From, p.To)...)
	} else {
		for _, row := range recurringRows {
			result = append(result, todoFromRow(row))
		}
	}

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

	set.DTStart(anchor)
	for _, ex := range td.ParseExDates() {
		set.ExDate(ex)
	}
	for _, rd := range td.ParseRDates() {
		set.RDate(rd)
	}

	occurrences := set.Between(from, to, true)
	// Enforce half-open upper bound: exclude occurrences exactly at 'to'.
	filteredOcc := occurrences[:0]
	for _, occ := range occurrences {
		if occ.Before(to) {
			filteredOcc = append(filteredOcc, occ)
		}
	}
	occurrences = filteredOcc

	var instances []ExpandedTodo
	for _, occ := range occurrences {
		isRDate := false
		for _, rd := range td.ParseRDates() {
			if occ.Equal(rd) {
				isRDate = true
				break
			}
		}
		instances = append(instances, ExpandedTodo{
			Todo:         td,
			InstanceTime: occ,
			IsOverride:   isRDate,
		})
	}
	return instances
}
