package recurrence

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
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

// inWindow reports whether an occurrence at t falls within the half-open
// window [from, to). Recurrence ranges are half-open everywhere in chroncal.
func inWindow(t, from, to time.Time) bool {
	return !t.Before(from) && t.Before(to)
}

// overlapsWindow reports whether the half-open interval [start, end) intersects
// [from, to). This matches the SQL range predicate (start_time < to AND
// end_time > from) used for non-recurring events, so a multi-day override that
// spans into the window is not dropped just because its start precedes it.
func overlapsWindow(start, end, from, to time.Time) bool {
	return start.Before(to) && end.After(from)
}

// eventOccursAt reports whether the recurring master evt produces an occurrence
// whose instance key equals recurrenceID. An override whose RECURRENCE-ID is not
// a genuine occurrence of its master is an orphan — left behind when a series is
// truncated or split — and is not part of the recurrence set, so it must not be
// expanded. The comparison uses the same instance key the suppression map keys
// on (InstanceTime.UTC() formatted as RFC 3339 vs the raw recurrence_id string),
// so suppression and orphan-detection can never disagree about a given slot.
func eventOccursAt(evt event.Event, recurrenceID string) bool {
	t, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return false
	}
	for _, inst := range ExpandEvent(evt, t.Add(-time.Second), t.Add(time.Second)) {
		if inst.InstanceTime.UTC().Format(time.RFC3339) == recurrenceID {
			return true
		}
	}
	return false
}

// todoOccursAt is the todo analogue of eventOccursAt.
func todoOccursAt(td todo.Todo, recurrenceID string) bool {
	t, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return false
	}
	for _, inst := range ExpandTodo(td, t.Add(-time.Second), t.Add(time.Second)) {
		if inst.InstanceTime.UTC().Format(time.RFC3339) == recurrenceID {
			return true
		}
	}
	return false
}

// journalOccursAt is the journal analogue of eventOccursAt.
func journalOccursAt(j journal.Journal, recurrenceID string) bool {
	t, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return false
	}
	for _, inst := range ExpandJournal(j, t.Add(-time.Second), t.Add(time.Second)) {
		if inst.InstanceTime.UTC().Format(time.RFC3339) == recurrenceID {
			return true
		}
	}
	return false
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

		// Fetch overrides for this master.
		overrides, _ := s.q.ListOverridesByUID(ctx, row.Uid)
		overridden := make(map[string]struct{}, len(overrides))
		for _, o := range overrides {
			overridden[o.RecurrenceID] = struct{}{}
		}

		// Emit master instances, skipping any slot that has been overridden;
		// the override is emitted separately below at its own occurrence time.
		for _, inst := range expanded {
			instKey := inst.InstanceTime.UTC().Format(time.RFC3339)
			if _, ok := overridden[instKey]; ok {
				continue
			}
			results = append(results, inst)
		}

		// Emit overrides whose own occurrence falls within [from, to). A moved
		// occurrence belongs to its new day, not the day of the slot it replaced.
		for _, o := range overrides {
			if strings.EqualFold(o.Status, "CANCELLED") {
				continue
			}
			oe := eventFromRow(o)
			if !overlapsWindow(oe.StartTime, oe.EndTime, from, to) {
				continue
			}
			if !eventOccursAt(evt, o.RecurrenceID) {
				continue // orphan override (no matching master occurrence)
			}
			results = append(results, ExpandedEvent{
				Event:        oe,
				InstanceTime: oe.StartTime,
			})
		}
	}

	if len(results) > 0 {
		ids := make([]int64, len(results))
		for i := range results {
			ids[i] = results[i].ID
		}

		if !o.skipCategories {
			cats, err := s.q.ListCategoriesByEventIDs(ctx, ids)
			if err == nil {
				catMap := make(map[int64][]string, len(results))
				for _, c := range cats {
					catMap[c.EventID] = append(catMap[c.EventID], c.Category)
				}
				for i := range results {
					if c, ok := catMap[results[i].ID]; ok {
						results[i].Categories = strings.Join(c, ",")
					}
				}
			}
		}

		atts, err := s.q.ListAttendeesByEventIDs(ctx, ids)
		if err == nil {
			attMap := make(map[int64][]model.Attendee, len(results))
			for _, a := range atts {
				attMap[a.EventID] = append(attMap[a.EventID], attendeeFromStorage(a))
			}
			for i := range results {
				if a, ok := attMap[results[i].ID]; ok {
					results[i].Attendees = a
				}
			}
		}
	}

	// Order by instance day, placing all-day events before timed events on
	// the same day, then timed events by start time. SQL-level ordering is
	// not sufficient because recurring instances are generated in Go.
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		ad := a.InstanceTime.Local().Truncate(24 * time.Hour)
		bd := b.InstanceTime.Local().Truncate(24 * time.Hour)
		if !ad.Equal(bd) {
			return ad.Before(bd)
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		return a.InstanceTime.Before(b.InstanceTime)
	})

	return results, nil
}

func attendeeFromStorage(r storage.EventAttendee) model.Attendee {
	return model.Attendee{
		ID:            r.ID,
		EventID:       r.EventID,
		Email:         r.Email,
		Name:          storage.NullableToString(r.Name),
		RSVPStatus:    r.RsvpStatus,
		Role:          r.Role,
		Organizer:     r.Organizer == 1,
		CUType:        storage.NullableToString(r.Cutype),
		RSVPRequested: strings.EqualFold(storage.NullableToString(r.Rsvp), "TRUE"),
		SentBy:        storage.NullableToString(r.SentBy),
		DelegatedTo:   storage.NullableToString(r.DelegatedTo),
		DelegatedFrom: storage.NullableToString(r.DelegatedFrom),
		Member:        storage.NullableToString(r.Member),
		Dir:           storage.NullableToString(r.Dir),
		Language:      storage.NullableToString(r.Language),
	}
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
		overridden := make(map[string]struct{}, len(overrides))
		for _, o := range overrides {
			overridden[o.RecurrenceID] = struct{}{}
		}

		// Emit master instances, skipping any slot that has been overridden;
		// the override is emitted separately below at its own occurrence time.
		for _, inst := range expanded {
			instKey := inst.InstanceTime.UTC().Format(time.RFC3339)
			if _, ok := overridden[instKey]; ok {
				continue
			}
			e := inst.Event
			dur := e.EndTime.Sub(e.StartTime)
			e.StartTime = inst.InstanceTime
			e.EndTime = inst.InstanceTime.Add(dur)
			result = append(result, e)
		}

		// Emit overrides whose own occurrence falls within [from, to). A moved
		// occurrence belongs to its new day, not the day of the slot it replaced.
		for _, o := range overrides {
			if strings.EqualFold(o.Status, "CANCELLED") {
				continue // CANCELLED override suppresses the instance.
			}
			oe := eventFromRow(o)
			if !overlapsWindow(oe.StartTime, oe.EndTime, from, to) {
				continue
			}
			if !eventOccursAt(evt, o.RecurrenceID) {
				continue // orphan override (no matching master occurrence)
			}
			result = append(result, oe)
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
		overridden := make(map[string]struct{}, len(overrides))
		for _, o := range overrides {
			overridden[o.RecurrenceID] = struct{}{}
		}

		// Emit master instances, skipping any slot that has been overridden;
		// the override is emitted separately below at its own occurrence time.
		for _, inst := range expanded {
			instKey := inst.InstanceTime.UTC().Format(time.RFC3339)
			if _, ok := overridden[instKey]; ok {
				continue
			}
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

		// Emit overrides whose own occurrence falls within [from, to). A moved
		// occurrence belongs to its new day, not the day of the slot it replaced.
		for _, o := range overrides {
			if strings.EqualFold(o.Status, "CANCELLED") {
				continue // CANCELLED override suppresses the instance.
			}
			if !todoOccursAt(td, o.RecurrenceID) {
				continue // orphan override (no matching master occurrence)
			}
			ot := todoFromRow(o)
			anchor := ot.ParseStartDate()
			if anchor.IsZero() {
				anchor = ot.ParseDueDate()
			}
			if anchor.IsZero() {
				// No datable anchor: fall back to the replaced slot for the
				// window check. recurrence_id parses here because todoOccursAt
				// above already returned true for it.
				anchor, _ = timeutil.ParseRecurrenceID(o.RecurrenceID)
			}
			if !inWindow(anchor, from, to) {
				continue
			}
			result = append(result, ot)
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
	CalendarID     int64
	Status         string
	Category       string
	From           time.Time
	To             time.Time
	IncludeDeleted bool
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
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		Category:       p.Category,
		FromTime:       fromStr,
		ToTime:         toStr,
		IncludeDeleted: p.IncludeDeleted,
	})
	if err != nil {
		return nil, err
	}

	var result []event.Event
	for _, row := range rangeRows {
		result = append(result, eventFromRow(row))
	}

	recurringRows, err := s.q.ListRecurringEventsFiltered(ctx, storage.EventFilterParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		Category:       p.Category,
		IncludeDeleted: p.IncludeDeleted,
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
	// IncludeDeleted, when true, returns soft-deleted todos alongside live
	// rows. Default (false) hides them, matching the live-query contract
	// every other service method honors.
	IncludeDeleted bool
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
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		HideCompleted:  hideCompleted,
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

	recurringRows, err := s.q.ListRecurringTodosFiltered(ctx, storage.ListRecurringTodosFilteredParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		HideCompleted:  hideCompleted,
		IncludeDeleted: p.IncludeDeleted,
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

// ── Journal recurrence ────────────────────────────────────────────────

// ExpandedJournal represents a single occurrence of a (possibly recurring) journal.
type ExpandedJournal struct {
	journal.Journal
	InstanceTime time.Time
	IsOverride   bool
}

// JournalListParams holds composable filters for listing journals.
type JournalListParams struct {
	CalendarID int64
	Status     string
	From       time.Time
	To         time.Time
	// IncludeDeleted, when true, returns soft-deleted journals alongside
	// live rows. Default (false) hides them.
	IncludeDeleted bool
}

func journalFromRow(row storage.Journal) journal.Journal {
	return journal.Journal{
		ID:             row.ID,
		UID:            row.Uid,
		CalendarID:     row.CalendarID,
		Summary:        row.Summary,
		Description:    storage.NullableToString(row.Description),
		StartDate:      storage.NullableToString(row.StartDate),
		Status:         row.Status,
		Class:          row.Class,
		URL:            storage.NullableToString(row.Url),
		RecurrenceRule: storage.NullableToString(row.RecurrenceRule),
		Timezone:       storage.NullableToString(row.Timezone),
		Sequence:       row.Sequence,
		ExDates:        storage.NullableToString(row.Exdates),
		RDates:         storage.NullableToString(row.Rdates),
		RecurrenceID:   row.RecurrenceID,
		DtStamp:        storage.NullableToString(row.Dtstamp),
		CreatedAt:      timeutil.ParseDateTime(row.CreatedAt),
		UpdatedAt:      timeutil.ParseDateTime(row.UpdatedAt),
	}
}

func (s *Service) populateJournalCategories(ctx context.Context, journals []journal.Journal) {
	if len(journals) == 0 {
		return
	}
	ids := make([]int64, len(journals))
	for i := range journals {
		ids[i] = journals[i].ID
	}
	rows, err := s.q.ListCategoriesByJournalIDs(ctx, ids)
	if err != nil {
		return
	}
	catMap := make(map[int64][]string, len(journals))
	for _, r := range rows {
		catMap[r.JournalID] = append(catMap[r.JournalID], r.Category)
	}
	for i := range journals {
		if cats, ok := catMap[journals[i].ID]; ok {
			journals[i].Categories = strings.Join(cats, ",")
		}
	}
}

// ExpandJournal generates all occurrences of a journal within a date range.
func ExpandJournal(j journal.Journal, from, to time.Time) []ExpandedJournal {
	anchor := j.ParseStartDate()
	if anchor.IsZero() {
		return nil
	}

	if j.RecurrenceRule == "" {
		if anchor.Before(from) || !anchor.Before(to) {
			return nil
		}
		return []ExpandedJournal{{Journal: j, InstanceTime: anchor}}
	}

	rruleStr := "RRULE:" + j.RecurrenceRule
	set, err := rrule.StrToRRuleSet(rruleStr)
	if err != nil {
		if anchor.Before(from) || !anchor.Before(to) {
			return nil
		}
		return []ExpandedJournal{{Journal: j, InstanceTime: anchor}}
	}

	loc := tzForExpansion(j.Timezone)
	dtstart := anchor
	localFrom, localTo := from, to
	if loc != nil {
		dtstart = dtstart.In(loc)
		localFrom = from.In(loc)
		localTo = to.In(loc)
	}

	set.DTStart(dtstart)
	for _, ex := range j.ParseExDates() {
		if loc != nil {
			ex = ex.In(loc)
		}
		set.ExDate(ex)
	}
	rdates := j.ParseRDates()
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

	var instances []ExpandedJournal
	for _, occ := range filteredOcc {
		_, isRDate := rdateSet[occ]
		instances = append(instances, ExpandedJournal{Journal: j, InstanceTime: occ.UTC(), IsOverride: isRDate})
	}
	return instances
}

func (s *Service) expandRecurringJournalRows(ctx context.Context, rows []storage.Journal, from, to time.Time) []journal.Journal {
	var result []journal.Journal
	for _, row := range rows {
		j := journalFromRow(row)
		expanded := ExpandJournal(j, from, to)

		overrides, _ := s.q.ListJournalOverridesByUID(ctx, row.Uid)
		overridden := make(map[string]struct{}, len(overrides))
		for _, o := range overrides {
			overridden[o.RecurrenceID] = struct{}{}
		}

		// Emit master instances, skipping any slot that has been overridden;
		// the override is emitted separately below at its own occurrence time.
		for _, inst := range expanded {
			instKey := inst.InstanceTime.UTC().Format(time.RFC3339)
			if _, ok := overridden[instKey]; ok {
				continue
			}
			jj := inst.Journal
			anchor := jj.ParseStartDate()
			if !anchor.IsZero() && jj.StartDate != "" {
				offset := inst.InstanceTime.Sub(anchor)
				newStart := anchor.Add(offset)
				if timeutil.IsDateOnly(jj.StartDate) {
					jj.StartDate = newStart.Format("2006-01-02")
				} else {
					jj.StartDate = newStart.Format(time.RFC3339)
				}
			}
			result = append(result, jj)
		}

		// Emit overrides whose own occurrence falls within [from, to). A moved
		// occurrence belongs to its new day, not the day of the slot it replaced.
		for _, o := range overrides {
			if strings.EqualFold(o.Status, "CANCELLED") {
				continue
			}
			if !journalOccursAt(j, o.RecurrenceID) {
				continue // orphan override (no matching master occurrence)
			}
			oj := journalFromRow(o)
			anchor := oj.ParseStartDate()
			if anchor.IsZero() {
				// No datable anchor: fall back to the replaced slot for the
				// window check. recurrence_id parses here because journalOccursAt
				// above already returned true for it.
				anchor, _ = timeutil.ParseRecurrenceID(o.RecurrenceID)
			}
			if !inWindow(anchor, from, to) {
				continue
			}
			result = append(result, oj)
		}
	}
	return result
}

// ListFilteredJournals returns journals matching all supplied filters. When a
// date range is provided, recurring journals are expanded; otherwise master
// entries are returned as-is.
func (s *Service) ListFilteredJournals(ctx context.Context, p JournalListParams) ([]journal.Journal, error) {
	fromStr := ""
	toStr := ""
	hasRange := !p.From.IsZero() || !p.To.IsZero()
	if !p.From.IsZero() {
		fromStr = p.From.Format("2006-01-02")
	}
	if !p.To.IsZero() {
		toStr = p.To.Format("2006-01-02")
	}

	rows, err := s.q.ListJournalsFiltered(ctx, storage.ListJournalsFilteredParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		FromDate:       fromStr,
		ToDate:         toStr,
		IncludeDeleted: p.IncludeDeleted,
	})
	if err != nil {
		return nil, err
	}

	var result []journal.Journal
	for _, row := range rows {
		result = append(result, journalFromRow(row))
	}

	recurringRows, err := s.q.ListRecurringJournalsFiltered(ctx, storage.ListRecurringJournalsFilteredParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		IncludeDeleted: p.IncludeDeleted,
	})
	if err != nil {
		return nil, err
	}
	if hasRange {
		result = append(result, s.expandRecurringJournalRows(ctx, recurringRows, p.From, p.To)...)
	} else {
		for _, row := range recurringRows {
			result = append(result, journalFromRow(row))
		}
	}

	s.populateJournalCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		di := result[i].ParseStartDate()
		dj := result[j].ParseStartDate()
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
