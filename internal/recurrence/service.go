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
// end_time > from) used for non-recurring events, so a multi-day instance or
// override that spans into the window is not dropped just because its start
// precedes it. Regular RRULE instances are filtered by this same overlap in
// ExpandEvent/ExpandTodo, generating from from-duration so a straddling
// occurrence is produced before being kept.
//
// A zero end (e.g. an override persisted with a blank end_time) is treated as
// instantaneous and matched by its start alone, so the occurrence is not
// silently dropped together with the master slot it replaces.
func overlapsWindow(start, end, from, to time.Time) bool {
	if end.IsZero() {
		return inWindow(start, from, to)
	}
	return start.Before(to) && end.After(from)
}

// keepOccurrence reports whether an expanded occurrence at occ with instance
// duration dur belongs in the half-open window [from, to): its [occ, occ+dur)
// interval overlaps the window, or (for a zero-duration occurrence whose open
// end boundary overlapsWindow would reject) occ itself falls inside it.
func keepOccurrence(occ time.Time, dur time.Duration, from, to time.Time) bool {
	return overlapsWindow(occ, occ.Add(dur), from, to) || inWindow(occ, from, to)
}

// canonicalRecurrenceID normalizes a stored recurrence_id to the same UTC
// RFC 3339 form used for expanded instance keys, so a date-only or zoned id
// compares equal to the occurrence it identifies. Suppression and orphan
// detection must use the same normalization (or neither) to stay in agreement;
// falls back to the raw string when it cannot be parsed.
func canonicalRecurrenceID(rid string) string {
	if t, err := timeutil.ParseRecurrenceID(rid); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return rid
}

// occursAt reports whether the recurring set produces an occurrence whose
// instance key equals recurrenceID. An override whose RECURRENCE-ID is not a
// genuine occurrence of its master is an orphan — left behind when a series is
// truncated or split — and is not part of the recurrence set, so it must not be
// expanded. The comparison uses the same instance key the suppression map keys
// on (InstanceTime.UTC() formatted as RFC 3339 vs the raw recurrence_id string),
// so suppression and orphan-detection can never disagree about a given slot.
//
// The set must be built with includeExDates=false: a RECURRENCE-ID override wins
// over its slot, so an EXDATE for the same slot must not make a legitimate
// override look like an orphan.
func (rs rruleSet) occursAt(recurrenceID string) bool {
	t, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return false
	}
	want := t.UTC().Format(time.RFC3339)
	local := t
	if rs.loc != nil {
		local = t.In(rs.loc)
	}
	for _, occ := range rs.set.Between(local.Add(-time.Second), local.Add(time.Second), true) {
		if occ.UTC().Format(time.RFC3339) == want {
			return true
		}
	}
	return false
}

// occChecker tests whether an override's RECURRENCE-ID names a genuine
// occurrence of its master. The master's RRULE is parsed once (EXDATEs ignored)
// and reused across all of the master's overrides, avoiding a re-parse per
// override. anchor is the master's own occurrence instant, used as the sole
// occurrence when the master is non-recurring or its RRULE fails to parse.
type occChecker struct {
	rs     rruleSet
	anchor time.Time
}

func (c occChecker) occursAt(recurrenceID string) bool {
	if c.rs.set != nil {
		return c.rs.occursAt(recurrenceID)
	}
	// Non-recurring / invalid RRULE master: the only occurrence is its anchor.
	t, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil || c.anchor.IsZero() {
		return false
	}
	return c.anchor.UTC().Format(time.RFC3339) == t.UTC().Format(time.RFC3339)
}

// rruleSet is a master's parsed RRULE plus the context needed to expand it or
// test individual occurrences. The RRULE is parsed once so the set can be reused
// for every occurrence and override of a master instead of re-parsing per call.
type rruleSet struct {
	set      *rrule.Set
	loc      *time.Location
	rdateSet map[string]struct{}
	dur      time.Duration
}

// newRRuleSet parses an "RRULE:"-prefixed rule anchored at dtstart in timezone
// tz, applying exDates/rDates. includeExDates controls whether the EXDATEs are
// applied: occurrence expansion needs them, but orphan detection must not (see
// occursAt). ok is false only when there is truly nothing to expand (rule is
// empty AND no rDates); callers fall back to a single instance in that case.
//
// RFC 5545 §3.8.5.2 permits RDATE-only recurrence with no RRULE. When rule is
// empty but rDates is non-empty, an empty rrule.Set is built and DTSTART is
// added as an implicit occurrence (RFC 5545 makes DTSTART an occurrence of the
// recurrence set even without an RRULE). DTSTART is intentionally excluded from
// rdateSet so it is not mislabelled IsOverride.
func newRRuleSet(rule, tz string, dtstart time.Time, dur time.Duration, exDates, rDates []time.Time, includeExDates bool) (rruleSet, bool) {
	// Nothing to expand: no RRULE and no explicit RDATEs.
	if rule == "" && len(rDates) == 0 {
		return rruleSet{}, false
	}
	var set *rrule.Set
	if rule != "" {
		var err error
		set, err = rrule.StrToRRuleSet("RRULE:" + rule)
		if err != nil {
			return rruleSet{}, false
		}
	} else {
		// RDATE-only: start with an empty set; occurrences come from RDates.
		set = &rrule.Set{}
	}
	loc := tzForExpansion(tz)
	if loc != nil {
		dtstart = dtstart.In(loc)
	}
	set.DTStart(dtstart)
	if includeExDates {
		for _, ex := range exDates {
			if loc != nil {
				ex = ex.In(loc)
			}
			set.ExDate(ex)
		}
		// Zone-skew safety net: EXDATEs written by older importers (TZID
		// ignored, pre-v0.5.1) or from transiently TZID-less server bodies
		// carry the zone-local wall clock mis-tagged as UTC, so they miss the
		// occurrence they were meant to exclude and a server-cancelled
		// instance keeps rendering forever — etag-gated sync never re-imports
		// an unchanged resource to repair them. Also exclude each EXDATE's
		// UTC wall clock reinterpreted in the expansion zone; for a healthy
		// EXDATE the reinterpreted instant matches no occurrence and is a
		// no-op. Date-only (all-day) EXDATEs are exact by construction and
		// midnight-UTC-tagged, so reinterpreting them would shift the day —
		// skip them.
		if loc != nil {
			for _, ex := range exDates {
				if timeutil.IsDateOnlyTime(ex) {
					continue
				}
				u := ex.UTC()
				reinterp := time.Date(u.Year(), u.Month(), u.Day(),
					u.Hour(), u.Minute(), u.Second(), u.Nanosecond(), loc)
				if !reinterp.Equal(ex) {
					set.ExDate(reinterp)
				}
			}
		}
	}
	for _, rd := range rDates {
		if loc != nil {
			rd = rd.In(loc)
		}
		set.RDate(rd)
	}
	// For RDATE-only sets, rrule.Set won't produce DTSTART without an RRULE.
	// Add DTSTART as an explicit RDate so it appears in expansion. It is not
	// added to rdateSet so the DTSTART occurrence is not mislabelled IsOverride.
	if rule == "" {
		set.RDate(dtstart)
	}
	if dur < 0 {
		dur = 0
	}
	return rruleSet{set: set, loc: loc, rdateSet: buildRDateSet(rDates), dur: dur}, true
}

// eventOverridesByUID fetches every override for the given recurring masters in
// a single query and groups them by master UID, avoiding one SELECT per master.
// A failed fetch is propagated, not swallowed: rendering a master as if it had
// no overrides would silently show a stale, un-overridden series.
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
	byUID := make(map[string][]storage.Event, len(rows))
	for _, r := range rows {
		byUID[r.Uid] = append(byUID[r.Uid], r)
	}
	return byUID, nil
}

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
		byUID[r.Uid] = append(byUID[r.Uid], r)
	}
	return byUID, nil
}

// journalOverridesByUID is the journal analogue of eventOverridesByUID.
func (s *Service) journalOverridesByUID(ctx context.Context, masters []storage.Journal) (map[string][]storage.Journal, error) {
	if len(masters) == 0 {
		return nil, nil
	}
	uids := make([]string, len(masters))
	for i, m := range masters {
		uids[i] = m.Uid
	}
	rows, err := s.q.ListJournalOverridesByUIDs(ctx, uids)
	if err != nil {
		return nil, err
	}
	byUID := make(map[string][]storage.Journal, len(rows))
	for _, r := range rows {
		byUID[r.Uid] = append(byUID[r.Uid], r)
	}
	return byUID, nil
}

// cancelledRecurringMaster reports whether the row is a recurring master that
// has been cancelled. Cancelling a recurring series cancels the whole series, so
// such a master expands to no occurrences. Because display, alarms, and
// free/busy all flow through the Expand* functions, all three intentionally see
// nothing for a cancelled series — including any still-CONFIRMED override, which
// is dropped with the series (matching Google/iCloud whole-series-cancel
// semantics). Non-recurring cancelled events are left untouched for the caller
// to show or hide. ICS export deliberately bypasses this via a status-stripped
// probe so a CANCELLED master still round-trips (see ExportExpandedByDateRange).
// RecurrenceRule and Status are exported strings on event.Event, todo.Todo, and
// journal.Journal alike.
func cancelledRecurringMaster(recurrenceRule, status string) bool {
	return recurrenceRule != "" && strings.EqualFold(status, "CANCELLED")
}

// isRDateOnlyMaster reports whether a storage row's rdates field marks it as an
// RDATE-only recurring master: no RRULE but at least one RDATE stored. Such
// rows must follow the recurring-expansion path even though recurrence_rule IS
// NULL, so Go-level filters that would otherwise include them as non-recurring
// singletons must skip them.
func isRDateOnlyMaster(rdates *string) bool {
	return rdates != nil && *rdates != ""
}

// rdateKey canonicalizes an RDATE/occurrence instant for membership lookups.
// The rrule iterator yields RDATE values truncated to whole seconds, so keying
// on a second-granularity UTC RFC 3339 string avoids sub-second precision or
// representation drift causing a missed match (which would mislabel an
// explicitly-added occurrence as a plain RRULE instance). See issue #128.
func rdateKey(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// buildRDateSet returns a canonical-string set of RDATE instants for O(1)
// IsOverride membership checks. Keys are timezone-independent (rdateKey
// normalizes to UTC), so no location conversion is needed here.
func buildRDateSet(rdates []time.Time) map[string]struct{} {
	set := make(map[string]struct{}, len(rdates))
	for _, rd := range rdates {
		set[rdateKey(rd)] = struct{}{}
	}
	return set
}

// between returns the occurrences in [from, to) whose [occ, occ+dur) interval
// overlaps the window, expanded once in the set's timezone. A multi-day instance
// whose start precedes 'from' can still overlap the window via its duration, so
// generation begins one instance-duration early and occurrences are kept by
// [start, end) overlap rather than start alone. Returned times are in the set's
// expansion zone; callers normalize to UTC.
func (rs rruleSet) between(from, to time.Time) []time.Time {
	localFrom, localTo := from, to
	if rs.loc != nil {
		localFrom = from.In(rs.loc)
		localTo = to.In(rs.loc)
	}
	occurrences := rs.set.Between(localFrom.Add(-rs.dur), localTo, true)
	kept := occurrences[:0]
	for _, occ := range occurrences {
		if keepOccurrence(occ, rs.dur, localFrom, localTo) {
			kept = append(kept, occ)
		}
	}
	return kept
}

// newEventRRuleSet parses evt's RRULE into a reusable rruleSet. See newRRuleSet
// for includeExDates.
func newEventRRuleSet(evt event.Event, includeExDates bool) (rruleSet, bool) {
	return newRRuleSet(evt.RecurrenceRule, evt.Timezone, evt.StartTime,
		evt.EndTime.Sub(evt.StartTime), evt.ParseExDates(), evt.ParseRDates(), includeExDates)
}

// newEventOccChecker builds a reusable orphan-detection checker for a recurring
// event master (EXDATEs ignored, so an EXDATE never hides a legitimate override).
// A cancelled series has no occurrences, so its checker matches nothing — this is
// what drops a still-CONFIRMED override along with the cancelled series.
func newEventOccChecker(evt event.Event) occChecker {
	if cancelledRecurringMaster(evt.RecurrenceRule, evt.Status) {
		return occChecker{}
	}
	rs, _ := newEventRRuleSet(evt, false)
	return occChecker{rs: rs, anchor: evt.StartTime}
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
	rs, ok := newEventRRuleSet(evt, true)
	if !ok {
		return singleEventInstance(evt, from, to)
	}

	var instances []ExpandedEvent
	for _, occ := range rs.between(from, to) {
		_, isRDate := rs.rdateSet[rdateKey(occ)]
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

// sortExpandedEvents orders instances by their local calendar day, placing
// all-day events before timed events on the same day, then timed events by
// start time. SQL-level ordering is not sufficient because recurring instances
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

// recurringKind bundles the entity-specific operations expandRecurringRowsBy
// needs to merge a recurring master with its overrides. Row is the storage row,
// Model the domain model, and Inst the Expand* result (which carries the model
// plus its InstanceTime).
type recurringKind[Row any, Model any, Inst any] struct {
	fromRow       func(Row) Model
	expand        func(Model, time.Time, time.Time) []Inst
	instTime      func(Inst) time.Time
	applyInstance func(Inst) Model // master instance adjusted to its occurrence time
	uid           func(Row) string
	status        func(Row) string
	recurrenceID  func(Row) string
	// overridesByUID batch-fetches every master's overrides in one query,
	// grouped by UID, and propagates any fetch error.
	overridesByUID func(context.Context, []Row) (map[string][]Row, error)
	// newOccChecker builds an orphan-detection checker for a master, parsing
	// its RRULE once for reuse across all of its overrides.
	newOccChecker func(master Model) occChecker
	// emitOverride builds an override's model and reports whether its own
	// occurrence falls within [from, to).
	emitOverride func(o Row, from, to time.Time) (Model, bool)
}

// expandRecurringRowsBy expands recurring master rows into per-occurrence
// Models, applying overrides. For each master, an override (a row with a
// matching RECURRENCE-ID) suppresses the original RRULE instance and is emitted
// separately at its own occurrence time. CANCELLED and orphan overrides are
// dropped. This is the shared engine behind the event/todo/journal variants.
//
// Every master's overrides are fetched in a single batched query up front; a
// failed fetch is propagated so callers never render masters as if they had no
// overrides.
func expandRecurringRowsBy[Row any, Model any, Inst any](ctx context.Context, k recurringKind[Row, Model, Inst], rows []Row, from, to time.Time) ([]Model, error) {
	overridesByUID, err := k.overridesByUID(ctx, rows)
	if err != nil {
		return nil, err
	}

	var result []Model
	for _, row := range rows {
		master := k.fromRow(row)
		expanded := k.expand(master, from, to)

		overrides := overridesByUID[k.uid(row)]
		overridden := make(map[string]struct{}, len(overrides))
		for _, o := range overrides {
			overridden[canonicalRecurrenceID(k.recurrenceID(o))] = struct{}{}
		}

		// Emit master instances, skipping any slot that has been overridden;
		// the override is emitted separately below at its own occurrence time.
		for _, inst := range expanded {
			instKey := k.instTime(inst).UTC().Format(time.RFC3339)
			if _, ok := overridden[instKey]; ok {
				continue
			}
			result = append(result, k.applyInstance(inst))
		}

		// Overrides are the exception, not the rule; skip the override work
		// (including the second RRULE parse the checker needs) for the common
		// case of a master with none.
		if len(overrides) == 0 {
			continue
		}

		// Emit overrides whose own occurrence falls within [from, to). A moved
		// occurrence belongs to its new day, not the day of the slot it replaced.
		// The cheap window check (in emitOverride) runs before the orphan probe;
		// the master's RRULE is parsed once for all of its overrides.
		checker := k.newOccChecker(master)
		for _, o := range overrides {
			if strings.EqualFold(k.status(o), "CANCELLED") {
				continue // CANCELLED override suppresses the instance.
			}
			m, ok := k.emitOverride(o, from, to)
			if !ok {
				continue // override's own occurrence is outside the window
			}
			if !checker.occursAt(k.recurrenceID(o)) {
				continue // orphan override (no matching master occurrence)
			}
			result = append(result, m)
		}
	}
	return result, nil
}

// expandedEventKind drives expandRecurringRowsBy for ListExpandedEvents, which
// needs each occurrence's InstanceTime preserved. ExpandedEvent serves as both
// the master Model and the emitted output, so applyInstance is identity: the
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
		uid:            func(r storage.Event) string { return r.Uid },
		status:         func(r storage.Event) string { return r.Status },
		recurrenceID:   func(r storage.Event) string { return r.RecurrenceID },
		overridesByUID: s.eventOverridesByUID,
		newOccChecker:  func(m ExpandedEvent) occChecker { return newEventOccChecker(m.Event) },
		emitOverride: func(o storage.Event, from, to time.Time) (ExpandedEvent, bool) {
			oe := event.FromStorage(o)
			return ExpandedEvent{Event: oe, InstanceTime: oe.StartTime}, overlapsWindow(oe.StartTime, oe.EndTime, from, to)
		},
	}
}

// expandRecurringRows expands recurring event rows into Event instances with
// StartTime/EndTime adjusted to each occurrence. For each master, overrides
// (rows with a matching RECURRENCE-ID) replace the original RRULE instance.
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
		uid:            func(r storage.Event) string { return r.Uid },
		status:         func(r storage.Event) string { return r.Status },
		recurrenceID:   func(r storage.Event) string { return r.RecurrenceID },
		overridesByUID: s.eventOverridesByUID,
		newOccChecker:  newEventOccChecker,
		emitOverride: func(o storage.Event, from, to time.Time) (event.Event, bool) {
			oe := event.FromStorage(o)
			return oe, overlapsWindow(oe.StartTime, oe.EndTime, from, to)
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
// or "" for the zero time. Normalizing to UTC is required because these bounds
// are compared lexically against the UTC-stored start/end strings (issue #305):
// a non-UTC offset left in the formatted string breaks that comparison near
// window edges.
func rangeBoundUTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ExportExpandedByDateRange returns recurring event masters (not expanded
// instances) that have at least one occurrence in [from,to), merged with
// non-recurring events whose start_time is in range. This is for ICS export
// where the master VEVENT with RRULE should be emitted, not individual
// instances. All filters (calendar, category, status) are applied at the
// SQL level.
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

// populateCategories batch-loads categories for items and assigns the joined
// category string to each via setCats. idOf yields an item's primary key,
// fetch loads the join rows for a set of ids, and rowCat splits a join row into
// its (id, category) pair. A fetch error is swallowed: categories augment a
// listing rather than gate it, matching the per-domain behavior this unifies.
func populateCategories[T any, R any](
	ctx context.Context,
	items []T,
	idOf func(T) int64,
	fetch func(context.Context, []int64) ([]R, error),
	rowCat func(R) (id int64, category string),
	setCats func(item *T, joined string),
) {
	if len(items) == 0 {
		return
	}
	ids := make([]int64, len(items))
	for i := range items {
		ids[i] = idOf(items[i])
	}
	rows, err := fetch(ctx, ids)
	if err != nil {
		return
	}
	catMap := make(map[int64][]string, len(items))
	for _, r := range rows {
		id, cat := rowCat(r)
		catMap[id] = append(catMap[id], cat)
	}
	for i := range items {
		if cats, ok := catMap[idOf(items[i])]; ok {
			setCats(&items[i], timeutil.JoinCategoryList(cats))
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

// todoDuration is the START->DUE span used to keep a straddling occurrence; a
// due-only (point) todo has none.
func todoDuration(td todo.Todo) time.Duration {
	if start := td.ParseStartDate(); !start.IsZero() {
		if due := td.ParseDueDate(); !due.IsZero() && due.After(start) {
			return due.Sub(start)
		}
	}
	return 0
}

// newTodoRRuleSet parses td's RRULE into a reusable rruleSet. ok is false when
// the todo has no anchor (so it cannot be expanded).
func newTodoRRuleSet(td todo.Todo, includeExDates bool) (rruleSet, bool) {
	anchor := todoAnchor(td)
	if anchor.IsZero() {
		return rruleSet{}, false
	}
	return newRRuleSet(td.RecurrenceRule, td.Timezone, anchor,
		todoDuration(td), td.ParseExDates(), td.ParseRDates(), includeExDates)
}

// newTodoOccChecker builds a reusable orphan-detection checker for a recurring
// todo master. A cancelled series matches nothing (see newEventOccChecker).
func newTodoOccChecker(td todo.Todo) occChecker {
	if cancelledRecurringMaster(td.RecurrenceRule, td.Status) {
		return occChecker{}
	}
	rs, _ := newTodoRRuleSet(td, false)
	return occChecker{rs: rs, anchor: todoAnchor(td)}
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
	rs, ok := newTodoRRuleSet(td, true)
	if !ok {
		return singleTodoInstance(td, from, to)
	}

	var instances []ExpandedTodo
	for _, occ := range rs.between(from, to) {
		_, isRDate := rs.rdateSet[rdateKey(occ)]
		instances = append(instances, ExpandedTodo{
			Todo:         td,
			InstanceTime: occ.UTC(),
			IsOverride:   isRDate,
		})
	}
	return instances
}

// expandRecurringTodoRows expands recurring todo rows into Todo instances with
// DueDate/StartDate adjusted to each occurrence. For each master, overrides
// (rows with a matching RECURRENCE-ID) replace the original RRULE instance.
func (s *Service) expandRecurringTodoRows(ctx context.Context, rows []storage.Todo, from, to time.Time) ([]todo.Todo, error) {
	k := recurringKind[storage.Todo, todo.Todo, ExpandedTodo]{
		fromRow:  todoFromRow,
		expand:   ExpandTodo,
		instTime: func(i ExpandedTodo) time.Time { return i.InstanceTime },
		applyInstance: func(i ExpandedTodo) todo.Todo {
			t := i.Todo
			anchor := t.ParseStartDate()
			if anchor.IsZero() {
				anchor = t.ParseDueDate()
			}
			if !anchor.IsZero() {
				offset := i.InstanceTime.Sub(anchor)
				t.DueDate = shiftDateString(t.DueDate, t.ParseDueDate(), offset)
				t.StartDate = shiftDateString(t.StartDate, t.ParseStartDate(), offset)
			}
			return t
		},
		uid:            func(r storage.Todo) string { return r.Uid },
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
				// which fails keepOccurrence and is dropped (the orphan probe
				// that follows would drop it too).
				anchor, _ = timeutil.ParseRecurrenceID(o.RecurrenceID)
			}
			// Keep on [START, DUE) interval overlap (honoring todoDuration), so a
			// multi-day override whose START precedes the window but whose DUE
			// falls inside it is not dropped -- matching the master-occurrence
			// path (keepOccurrence/between) and the event override path.
			return ot, keepOccurrence(anchor, todoDuration(ot), from, to)
		},
	}
	return expandRecurringRowsBy(ctx, k, rows, from, to)
}

// shiftDateString returns value advanced by offset, preserving its date-only or
// RFC 3339 representation. It returns value unchanged when it is empty or its
// parsed form (parsed) is zero, so a blank or unparseable field is left intact.
func shiftDateString(value string, parsed time.Time, offset time.Duration) string {
	if value == "" || parsed.IsZero() {
		return value
	}
	shifted := parsed.Add(offset)
	if timeutil.IsDateOnly(value) {
		return shifted.Format("2006-01-02")
	}
	return shifted.Format(time.RFC3339)
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
// status, and date-range filters compose freely. When a date range is
// provided, recurring events are expanded within it, with overrides applied;
// otherwise recurring masters are returned as-is, matching the
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
	// HideCancelled, when true, omits CANCELLED journals. Default (false)
	// returns every status, matching the iCal model where a cancelled
	// journal is still a real row the caller may want to see.
	HideCancelled bool
	From          time.Time
	To            time.Time
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
	populateCategories(ctx, journals,
		func(j journal.Journal) int64 { return j.ID },
		s.q.ListCategoriesByJournalIDs,
		func(r storage.JournalCategory) (int64, string) { return r.JournalID, r.Category },
		func(j *journal.Journal, joined string) { j.Categories = joined },
	)
}

// newJournalRRuleSet parses j's RRULE into a reusable rruleSet. ok is false when
// the journal has no start date (so it cannot be expanded). Journals are points
// in time, so the set carries no duration.
func newJournalRRuleSet(j journal.Journal, includeExDates bool) (rruleSet, bool) {
	anchor := j.ParseStartDate()
	if anchor.IsZero() {
		return rruleSet{}, false
	}
	return newRRuleSet(j.RecurrenceRule, j.Timezone, anchor, 0,
		j.ParseExDates(), j.ParseRDates(), includeExDates)
}

// newJournalOccChecker builds a reusable orphan-detection checker for a
// recurring journal master. A cancelled series matches nothing (see
// newEventOccChecker).
func newJournalOccChecker(j journal.Journal) occChecker {
	if cancelledRecurringMaster(j.RecurrenceRule, j.Status) {
		return occChecker{}
	}
	rs, _ := newJournalRRuleSet(j, false)
	return occChecker{rs: rs, anchor: j.ParseStartDate()}
}

// singleJournalInstance returns j as a lone occurrence (non-recurring or
// unparseable RRULE) if its start date falls within [from, to).
func singleJournalInstance(j journal.Journal, from, to time.Time) []ExpandedJournal {
	anchor := j.ParseStartDate()
	if anchor.IsZero() || anchor.Before(from) || !anchor.Before(to) {
		return nil
	}
	return []ExpandedJournal{{Journal: j, InstanceTime: anchor}}
}

// ExpandJournal generates all occurrences of a journal within a date range.
func ExpandJournal(j journal.Journal, from, to time.Time) []ExpandedJournal {
	// A cancelled recurring master has no occurrences (see cancelledRecurringMaster).
	if cancelledRecurringMaster(j.RecurrenceRule, j.Status) {
		return nil
	}
	rs, ok := newJournalRRuleSet(j, true)
	if !ok {
		return singleJournalInstance(j, from, to)
	}

	var instances []ExpandedJournal
	for _, occ := range rs.between(from, to) {
		_, isRDate := rs.rdateSet[rdateKey(occ)]
		instances = append(instances, ExpandedJournal{Journal: j, InstanceTime: occ.UTC(), IsOverride: isRDate})
	}
	return instances
}

func (s *Service) expandRecurringJournalRows(ctx context.Context, rows []storage.Journal, from, to time.Time) ([]journal.Journal, error) {
	k := recurringKind[storage.Journal, journal.Journal, ExpandedJournal]{
		fromRow:  journalFromRow,
		expand:   ExpandJournal,
		instTime: func(i ExpandedJournal) time.Time { return i.InstanceTime },
		applyInstance: func(i ExpandedJournal) journal.Journal {
			jj := i.Journal
			if anchor := jj.ParseStartDate(); !anchor.IsZero() {
				jj.StartDate = shiftDateString(jj.StartDate, anchor, i.InstanceTime.Sub(anchor))
			}
			return jj
		},
		uid:            func(r storage.Journal) string { return r.Uid },
		status:         func(r storage.Journal) string { return r.Status },
		recurrenceID:   func(r storage.Journal) string { return r.RecurrenceID },
		overridesByUID: s.journalOverridesByUID,
		newOccChecker:  newJournalOccChecker,
		emitOverride: func(o storage.Journal, from, to time.Time) (journal.Journal, bool) {
			oj := journalFromRow(o)
			anchor := oj.ParseStartDate()
			if anchor.IsZero() {
				// No datable anchor: fall back to the replaced slot for the
				// window check. An unparseable recurrence_id leaves anchor zero,
				// which fails inWindow and is dropped (the orphan probe that
				// follows would drop it too).
				anchor, _ = timeutil.ParseRecurrenceID(o.RecurrenceID)
			}
			return oj, inWindow(anchor, from, to)
		},
	}
	return expandRecurringRowsBy(ctx, k, rows, from, to)
}

// ListFilteredJournals returns journals matching all supplied filters. When a
// date range is provided, recurring journals are expanded; otherwise master
// entries are returned as-is.
func (s *Service) ListFilteredJournals(ctx context.Context, p JournalListParams) ([]journal.Journal, error) {
	hideCancelled := int64(0)
	if p.HideCancelled {
		hideCancelled = 1
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

	rows, err := s.q.ListJournalsFiltered(ctx, storage.ListJournalsFilteredParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		HideCancelled:  hideCancelled,
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
		HideCancelled:  hideCancelled,
		IncludeDeleted: p.IncludeDeleted,
	})
	if err != nil {
		return nil, err
	}
	if hasRange {
		expanded, err := s.expandRecurringJournalRows(ctx, recurringRows, p.From, p.To)
		if err != nil {
			return nil, err
		}
		result = append(result, expanded...)
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
