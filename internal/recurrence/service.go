package recurrence

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// Service handles recurrence expansion and the cache.
type Service struct {
	db *sql.DB
	q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
}

// seriesKey identifies a recurring series. UID is unique per calendar
// (issue #756), so the key includes calendar_id.
func seriesKey(calendarID int64, uid string) string {
	return strconv.FormatInt(calendarID, 10) + "\x00" + uid
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
	// Do not convert for fixed-offset zones (no DST to handle).
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
// end_time > from) used for non-recurring events. A multi-day instance or
// override that spans into the window is then not dropped just because its
// start precedes it. Regular RRULE instances are filtered by this same overlap
// in ExpandEvent/ExpandTodo. Generation starts from from-duration so a
// occurrence that spans the window edge is produced before it is kept.
//
// A zero end (for example an override persisted with a blank end_time) is
// treated as instantaneous and matched by its start alone. The occurrence is
// then not dropped in silence together with the master slot it replaces.
func overlapsWindow(start, end, from, to time.Time) bool {
	if end.IsZero() {
		return inWindow(start, from, to)
	}
	return start.Before(to) && end.After(from)
}

// keepOccurrence reports whether an expanded occurrence at occ with instance
// duration dur belongs in the half-open window [from, to). Its [occ, occ+dur)
// interval overlaps the window. For a zero-duration occurrence whose open
// end boundary overlapsWindow would reject, occ itself falls inside it.
func keepOccurrence(occ time.Time, dur time.Duration, from, to time.Time) bool {
	return overlapsWindow(occ, occ.Add(dur), from, to) || inWindow(occ, from, to)
}

// canonicalRecurrenceID normalizes a stored recurrence_id to the same UTC
// RFC 3339 form used for expanded instance keys. A date-only or zoned id then
// compares equal to the occurrence it identifies. Suppression and orphan
// detection must use the same normalization (or neither) to stay in agreement.
// It falls back to the raw string when it cannot be parsed.
func canonicalRecurrenceID(rid string) string {
	if t, err := timeutil.ParseRecurrenceID(rid); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return rid
}

// occursAt reports whether the recurring set produces an occurrence whose
// instance key equals recurrenceID. An override whose RECURRENCE-ID is not a
// genuine occurrence of its master is an orphan. It is left behind when a
// series is truncated or split. It is not part of the recurrence set. It
// must not be expanded.
//
// The comparison uses the same instance key the suppression map keys on
// (InstanceTime.UTC() formatted as RFC 3339 vs the raw recurrence_id string).
// Suppression and orphan-detection then cannot disagree about a given slot.
//
// The set must be built with includeExDates=false. A RECURRENCE-ID override
// wins over its slot. An EXDATE for the same slot must not make a legitimate
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
// and reused across all of the master's overrides. There is no re-parse per
// override. anchor is the master's own occurrence instant. It is the sole
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
// for every occurrence and override of a master. There is no re-parse per call.
type rruleSet struct {
	set      *rrule.Set
	loc      *time.Location
	rdateSet map[string]struct{}
	dur      time.Duration
}

// newRRuleSet parses an "RRULE:"-prefixed rule anchored at dtstart in timezone
// tz, and applies exDates/rDates. includeExDates controls whether the EXDATEs
// are applied. Occurrence expansion needs them. Orphan detection must not (see
// occursAt). ok is false only when there is truly nothing to expand (rule is
// empty AND no rDates). Callers fall back to a single instance in that case.
//
// RFC 5545 §3.8.5.2 permits RDATE-only recurrence with no RRULE. When rule is
// empty but rDates is non-empty, an empty rrule.Set is built and DTSTART is
// added as an implicit occurrence. RFC 5545 makes DTSTART an occurrence of the
// recurrence set even without an RRULE. DTSTART is intentionally excluded from
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

// cancelledRecurringMaster reports whether the row is a recurring master that
// is cancelled. A cancel of a recurring series cancels the whole series.
// Such a master expands to no occurrences.
//
// Display, alarms, and free/busy all flow through the Expand* functions. All
// three intentionally see nothing for a cancelled series. Any still-CONFIRMED
// override is dropped with the series. That matches Google/iCloud
// whole-series-cancel semantics. Non-recurring cancelled events are left
// untouched for the caller to show or hide. ICS export bypasses this via a
// status-stripped probe so a CANCELLED master still round-trips (see
// ExportExpandedByDateRange). RecurrenceRule and Status are exported strings
// on event.Event, todo.Todo, and journal.Journal alike.
func cancelledRecurringMaster(recurrenceRule, status string) bool {
	return recurrenceRule != "" && strings.EqualFold(status, "CANCELLED")
}

// isRDateOnlyMaster reports whether a storage row's rdates field marks it as an
// RDATE-only recurring master: no RRULE but at least one RDATE stored. Such
// rows must follow the recurring-expansion path even though recurrence_rule IS
// NULL. Go-level filters that would otherwise include them as non-recurring
// singletons must skip them.
func isRDateOnlyMaster(rdates *string) bool {
	return rdates != nil && *rdates != ""
}

// rdateKey canonicalizes an RDATE/occurrence instant for membership lookups.
// The rrule iterator yields RDATE values truncated to whole seconds. A key
// on a second-granularity UTC RFC 3339 string then avoids sub-second precision
// or representation drift. A missed match would mislabel an explicitly-added
// occurrence as a plain RRULE instance. See issue #128.
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
// whose start precedes 'from' can still overlap the window via its duration.
// Generation then begins one instance-duration early. Occurrences are kept by
// [start, end) overlap rather than start alone. Returned times are in the set's
// expansion zone. Callers normalize to UTC.
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
// Models and applies overrides. For each master, an override is a row with a
// RECURRENCE-ID that matches. It suppresses the original RRULE instance. It
// is emitted separately at its own occurrence time. CANCELLED and orphan
// overrides are dropped. This is the shared engine behind the event, todo,
// and journal variants.
//
// Every master's overrides are fetched in a single batched query up front. A
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

// populateCategories batch-loads categories for items and assigns the joined
// category string to each via setCats. idOf yields an item's primary key.
// fetch loads the join rows for a set of ids. rowCat splits a join row into
// its (id, category) pair. A fetch error is swallowed. Categories augment a
// list rather than gate it. This matches the per-domain behavior this unifies.
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

// shiftDateString returns value advanced by offset. It keeps its date-only or
// RFC 3339 representation. It returns value unchanged when it is empty or its
// parsed form (parsed) is zero. A blank or unparseable field is then left intact.
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
