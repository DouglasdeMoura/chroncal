package recurrence

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurset"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/uidmove"
)

// eventOverridesByUID fetches every override for the given recurring masters in
// a single query and groups them by master UID. There is no SELECT per master.
// A failed fetch is propagated, not swallowed. A render of a master as if it
// had no overrides would show a stale, un-overridden series in silence.
func (s *Service) eventOverridesByUID(ctx context.Context, masters []storage.Event) (map[string][]storage.Event, error) {
	if len(masters) == 0 {
		return nil, nil
	}
	uids := make([]string, len(masters))
	for i, m := range masters {
		uids[i] = m.Uid
	}
	rows, err := s.q.ListOverridesByUIDs(ctx, uids)
	if err != nil {
		return nil, err
	}
	ownerOf, err := s.legacyOwners(ctx, masters, rows)
	if err != nil {
		return nil, err
	}
	byUID := make(map[string][]storage.Event, len(rows))
	for _, r := range rows {
		owner, ok := ownerOf[r.ID]
		if !ok {
			owner = r.CalendarID
		}
		key := seriesKey(owner, r.Uid)
		byUID[key] = append(byUID[key], r)
	}
	// One row per occurrence and series, with the tie rule of the series
	// queries: the row beside the master of the series comes first, then
	// the lowest ID. The rows of a key beside its master hold the calendar
	// of the key. The common case reads no view, so the order is set here.
	for key, group := range byUID {
		if len(group) < 2 {
			continue
		}
		tie := func(r storage.Event) uidmove.OverrideTie {
			return uidmove.OverrideTie{RecurrenceID: r.RecurrenceID, BesideMaster: seriesKey(r.CalendarID, r.Uid) == key, ID: r.ID}
		}
		sort.SliceStable(group, func(i, j int) bool { return uidmove.TieBefore(tie(group[i]), tie(group[j])) })
		byUID[key] = storage.FirstPerRecurrenceID(group)
	}
	return byUID, nil
}

// legacyOwners gives the owner calendar of each override row that does not
// sit beside a loaded master. An override beside its own master belongs to
// that series, so the common case reads no view. Only the UIDs with a row
// in a calendar with no loaded master read the view event_series_rows. A
// row with no owner is not in the result.
func (s *Service) legacyOwners(ctx context.Context, masters, rows []storage.Event) (map[int64]int64, error) {
	loaded := make(map[string]bool, len(masters))
	for _, m := range masters {
		loaded[seriesKey(m.CalendarID, m.Uid)] = true
	}
	var uids []string
	away := make(map[string]bool)
	for _, r := range rows {
		if !loaded[seriesKey(r.CalendarID, r.Uid)] && !away[r.Uid] {
			away[r.Uid] = true
			uids = append(uids, r.Uid)
		}
	}
	if len(uids) == 0 {
		return nil, nil
	}
	owners, err := s.q.ListSeriesOwnersByUIDs(ctx, uids)
	if err != nil {
		return nil, err
	}
	ownerOf := make(map[int64]int64, len(owners))
	for _, o := range owners {
		ownerOf[o.ID] = o.OwnerCalendarID
	}
	return ownerOf, nil
}

// newEventRRuleSet parses evt's RRULE into a reusable recurset.Set for
// expansion, with the EXDATEs applied. The occurrence test of an override
// ignores the EXDATEs and uses recurset.NewChecker instead.
func newEventRRuleSet(evt event.Event) (recurset.Set, bool) {
	return recurset.New(evt.RecurrenceRule, evt.Timezone, evt.StartTime,
		evt.EndTime.Sub(evt.StartTime), evt.ParseExDates(), evt.ParseRDates())
}

// newEventOccChecker builds a reusable orphan-detection checker for a recurring
// event master (EXDATEs ignored, so an EXDATE never hides a legitimate override).
// A cancelled series has no occurrences, so its checker matches nothing — this is
// what drops a still-CONFIRMED override along with the cancelled series.
func newEventOccChecker(evt event.Event) recurset.Checker {
	if cancelledRecurringMaster(evt.RecurrenceRule, evt.Status) {
		return recurset.Checker{}
	}
	return recurset.NewChecker(evt.RecurrenceRule, evt.Timezone, evt.StartTime, evt.ParseRDates())
}

// singleEventInstance returns evt as a lone occurrence when it is non-recurring
// or carries an unparseable RRULE, if its start falls within [from, to).
func singleEventInstance(evt event.Event, from, to time.Time) []ExpandedEvent {
	if evt.StartTime.Before(from) || !evt.StartTime.Before(to) {
		return nil
	}
	return []ExpandedEvent{{Event: evt, InstanceTime: evt.StartTime}}
}

// ExpandEvent generates all occurrences of an event within a date range
// Returns instances even for non-recurring events (single instance)
func ExpandEvent(evt event.Event, from, to time.Time) []ExpandedEvent {
	if cancelledRecurringMaster(evt.RecurrenceRule, evt.Status) {
		return nil
	}
	rs, ok := newEventRRuleSet(evt)
	if !ok {
		return singleEventInstance(evt, from, to)
	}

	var instances []ExpandedEvent
	for _, occ := range rs.Between(from, to) {
		_, isRDate := rs.RDates[recurset.RDateKey(occ)]
		instances = append(instances, ExpandedEvent{
			Event:        evt,
			InstanceTime: occ.UTC(),
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
// does not need Event.Categories (for example alarm checks).
func SkipCategories() ExpandOption {
	return func(o *expandOptions) { o.skipCategories = true }
}

// ListExpandedEvents returns events with their instances in a date range.
// It uses filtered queries instead of a load of the entire table.
func (s *Service) ListExpandedEvents(ctx context.Context, from, to time.Time, opts ...ExpandOption) ([]ExpandedEvent, error) {
	var o expandOptions
	for _, fn := range opts {
		fn(&o)
	}
	// Non-recurring events in date range.
	rangeRows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime: to.UTC().Format(time.RFC3339),   // start_time < to
		EndTime:   from.UTC().Format(time.RFC3339), // end_time > from
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
		// Skip recurring masters (RRULE or RDATE-only) — handled via recurRows below.
		if row.RecurrenceID == "" && (row.RecurrenceRule != nil || isRDateOnlyMaster(row.Rdates)) {
			continue
		}
		// Skip overrides; they're merged during recurring expansion below.
		if row.RecurrenceID != "" {
			continue
		}
		evt := event.FromStorage(row)
		results = append(results, ExpandedEvent{
			Event:        evt,
			InstanceTime: evt.StartTime,
		})
	}

	// Merge recurring masters with their overrides through the shared engine.
	// expandedEventKind keeps ExpandedEvent as both the master Model and the
	// emitted output, so each occurrence's InstanceTime survives (unlike
	// expandRecurringRows, which folds the occurrence time into Event.Start/End).
	// The engine batch-fetches overrides and propagates any fetch error, so a
	// failed lookup never renders a master as if it had none.
	recurResults, err := expandRecurringRowsBy(ctx, expandedEventKind(s), recurRows, from, to)
	if err != nil {
		return nil, err
	}
	results = append(results, recurResults...)

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
						results[i].Categories = timeutil.JoinCategoryList(c)
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

	sortExpandedEvents(results)

	return results, nil
}

// sortExpandedEvents orders instances by their local calendar day. It places
// all-day events before timed events on the same day. Timed events then sort
// by start time. SQL-level order is not sufficient. Recurring instances
// are generated in Go.
func sortExpandedEvents(results []ExpandedEvent) {
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		ad := timeutil.LocalDay(a.InstanceTime)
		bd := timeutil.LocalDay(b.InstanceTime)
		if !ad.Equal(bd) {
			return ad.Before(bd)
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		return a.InstanceTime.Before(b.InstanceTime)
	})
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

// expandedEventKind drives expandRecurringRowsBy for ListExpandedEvents, which
// needs each occurrence's InstanceTime kept. ExpandedEvent serves as both
// the master Model and the emitted output, so applyInstance is identity. The
// per-occurrence InstanceTime that expandRecurringRows discards (it collapses
// the occurrence into Event.Start/End) is kept here.
func expandedEventKind(s *Service) recurringKind[storage.Event, ExpandedEvent, ExpandedEvent] {
	return recurringKind[storage.Event, ExpandedEvent, ExpandedEvent]{
		fromRow: func(r storage.Event) ExpandedEvent {
			return ExpandedEvent{Event: event.FromStorage(r)}
		},
		expand: func(m ExpandedEvent, from, to time.Time) []ExpandedEvent {
			return ExpandEvent(m.Event, from, to)
		},
		instTime:       func(i ExpandedEvent) time.Time { return i.InstanceTime },
		applyInstance:  func(i ExpandedEvent) ExpandedEvent { return i },
		uid:            func(r storage.Event) string { return seriesKey(r.CalendarID, r.Uid) },
		status:         func(r storage.Event) string { return r.Status },
		recurrenceID:   func(r storage.Event) string { return r.RecurrenceID },
		overridesByUID: s.eventOverridesByUID,
		newOccChecker:  func(m ExpandedEvent) recurset.Checker { return newEventOccChecker(m.Event) },
		emitOverride: func(o storage.Event, from, to time.Time) (ExpandedEvent, bool) {
			oe := event.FromStorage(o)
			return ExpandedEvent{Event: oe, InstanceTime: oe.StartTime}, recurset.OverlapsWindow(oe.StartTime, oe.EndTime, from, to)
		},
	}
}

// expandRecurringRows expands recurring event rows into Event instances with
// StartTime/EndTime adjusted to each occurrence. For each master, overrides
// (rows whose RECURRENCE-ID matches) replace the original RRULE instance.
func (s *Service) expandRecurringRows(ctx context.Context, rows []storage.Event, from, to time.Time) ([]event.Event, error) {
	k := recurringKind[storage.Event, event.Event, ExpandedEvent]{
		fromRow:  event.FromStorage,
		expand:   ExpandEvent,
		instTime: func(i ExpandedEvent) time.Time { return i.InstanceTime },
		applyInstance: func(i ExpandedEvent) event.Event {
			e := i.Event
			dur := e.EndTime.Sub(e.StartTime)
			e.StartTime = i.InstanceTime
			e.EndTime = i.InstanceTime.Add(dur)
			return e
		},
		uid:            func(r storage.Event) string { return seriesKey(r.CalendarID, r.Uid) },
		status:         func(r storage.Event) string { return r.Status },
		recurrenceID:   func(r storage.Event) string { return r.RecurrenceID },
		overridesByUID: s.eventOverridesByUID,
		newOccChecker:  newEventOccChecker,
		emitOverride: func(o storage.Event, from, to time.Time) (event.Event, bool) {
			oe := event.FromStorage(o)
			return oe, recurset.OverlapsWindow(oe.StartTime, oe.EndTime, from, to)
		},
	}
	return expandRecurringRowsBy(ctx, k, rows, from, to)
}

// ListExpandedByDateRange returns non-recurring events in [from,to) merged
// with expanded instances of recurring event masters. The returned events have
// StartTime/EndTime adjusted to the instance time and are sorted by StartTime.
func (s *Service) ListExpandedByDateRange(ctx context.Context, from, to time.Time) ([]event.Event, error) {
	rangeRows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime: to.UTC().Format(time.RFC3339),   // start_time < to
		EndTime:   from.UTC().Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}
	// Keep only non-recurring, non-RDATE-only masters from the date-range results.
	// RDATE-only masters (RecurrenceRule IS NULL but Rdates non-empty) must follow
	// the recurring-expansion path below, not be emitted as singletons here.
	var result []event.Event
	for _, row := range rangeRows {
		if row.RecurrenceRule == nil && row.RecurrenceID == "" && !isRDateOnlyMaster(row.Rdates) {
			result = append(result, event.FromStorage(row))
		}
	}

	recurringRows, err := s.q.ListRecurringEvents(ctx)
	if err != nil {
		return nil, err
	}
	expanded, err := s.expandRecurringRows(ctx, recurringRows, from, to)
	if err != nil {
		return nil, err
	}
	result = append(result, expanded...)

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

// rangeBoundUTC formats a date-range query bound as an RFC3339 string in UTC,
// or "" for the zero time. Normalize to UTC because these bounds are compared
// lexically against the UTC-stored start/end strings (issue #305). A non-UTC
// offset left in the formatted string breaks that comparison near window edges.
func rangeBoundUTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ExportExpandedByDateRange returns recurring event masters (not expanded
// instances) that have at least one occurrence in [from,to). It merges them
// with non-recurring events whose start_time is in range. This is for ICS
// export. Emit the master VEVENT with RRULE, not individual instances. All
// filters (calendar, category, status) are applied at the SQL level.
func (s *Service) ExportExpandedByDateRange(ctx context.Context, p ExportFilterParams) ([]event.Event, error) {
	fromStr := rangeBoundUTC(p.From)
	toStr := rangeBoundUTC(p.To)

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
			result = append(result, event.FromStorage(row))
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
		evt := event.FromStorage(row)
		// Export must emit a cancelled master (STATUS:CANCELLED is how a
		// downstream client is told to drop the series), so the in-range probe
		// ignores the cancelled-expansion guard — unlike display, which hides it.
		probe := evt
		probe.Status = ""
		if len(ExpandEvent(probe, p.From, p.To)) > 0 {
			result = append(result, evt)
		}
	}

	s.populateEventCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result, nil
}

// populateEventAttendees attaches guests in one batched query. event list JSON
// is the Chroncal Bar agenda payload; without this it omits invitations that
// event get already hydrates. Generated occurrences keep the master ID, so they
// receive the master's attendee rows. A load error leaves Attendees unset,
// matching populateEventCategories.
func (s *Service) populateEventAttendees(ctx context.Context, events []event.Event) {
	if len(events) == 0 {
		return
	}
	ids := make([]int64, len(events))
	for i := range events {
		ids[i] = events[i].ID
	}
	atts, err := s.q.ListAttendeesByEventIDs(ctx, ids)
	if err != nil {
		return
	}
	attMap := make(map[int64][]model.Attendee, len(events))
	for _, a := range atts {
		attMap[a.EventID] = append(attMap[a.EventID], attendeeFromStorage(a))
	}
	for i := range events {
		if a, ok := attMap[events[i].ID]; ok {
			events[i].Attendees = a
		}
	}
}

func (s *Service) populateEventCategories(ctx context.Context, events []event.Event) {
	populateCategories(ctx, events,
		func(e event.Event) int64 { return e.ID },
		s.q.ListCategoriesByEventIDs,
		func(r storage.EventCategory) (int64, string) { return r.EventID, r.Category },
		func(e *event.Event, joined string) { e.Categories = joined },
	)
}

// EventListParams holds composable filters for event lists.
type EventListParams struct {
	CalendarID     int64
	Status         string
	Category       string
	From           time.Time
	To             time.Time
	IncludeDeleted bool
}

// ListFilteredEvents returns events that match all supplied filters. Calendar,
// status, and date-range filters compose freely. When a date range is
// provided, recurring events are expanded within it, with overrides applied.
// Otherwise recurring masters are returned as-is. This matches the
// todo/journal contract.
func (s *Service) ListFilteredEvents(ctx context.Context, p EventListParams) ([]event.Event, error) {
	fromStr := rangeBoundUTC(p.From)
	toStr := rangeBoundUTC(p.To)
	hasRange := fromStr != "" || toStr != ""

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
		result = append(result, event.FromStorage(row))
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
	if hasRange {
		expanded, err := s.expandRecurringRows(ctx, recurringRows, p.From, p.To)
		if err != nil {
			return nil, err
		}
		result = append(result, expanded...)
	} else {
		for _, row := range recurringRows {
			result = append(result, event.FromStorage(row))
		}
	}

	s.populateEventCategories(ctx, result)
	s.populateEventAttendees(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result, nil
}
