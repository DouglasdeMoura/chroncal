// Package recurset parses the recurrence set of a master (RRULE, RDATE,
// and EXDATE) and tests whether a recurrence ID names an occurrence. It
// imports no domain package, so the domain services and the recurrence
// package can share the one parse rule.
package recurset

import (
	"time"

	"github.com/teambition/rrule-go"

	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// TZForExpansion returns the *time.Location to use for rrule expansion.
// If tz is a valid IANA timezone, expansion happens in that timezone so
// wall-clock times are preserved across DST transitions. Otherwise nil
// is returned and expansion happens in whatever timezone the times carry.
func TZForExpansion(tz string) *time.Location {
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

// Set is a master's parsed RRULE plus the context needed to expand it or
// test individual occurrences. The RRULE is parsed once so the set can be
// reused for every occurrence and override of a master. There is no
// re-parse per call.
type Set struct {
	Rule   *rrule.Set
	Loc    *time.Location
	RDates map[string]struct{}
	Dur    time.Duration
}

// New parses an "RRULE:"-prefixed rule anchored at dtstart in timezone tz,
// and applies exDates/rDates. includeExDates controls whether the EXDATEs
// are applied. Occurrence expansion needs them. Orphan detection must not
// (see OccursAt). ok is false only when there is truly nothing to expand
// (rule is empty AND no rDates). Callers fall back to a single instance in
// that case.
//
// RFC 5545 §3.8.5.2 permits RDATE-only recurrence with no RRULE. When rule
// is empty but rDates is non-empty, an empty rrule.Set is built and DTSTART
// is added as an implicit occurrence. RFC 5545 makes DTSTART an occurrence
// of the recurrence set even without an RRULE. DTSTART is intentionally
// excluded from RDates so it is not mislabelled IsOverride.
func New(rule, tz string, dtstart time.Time, dur time.Duration, exDates, rDates []time.Time, includeExDates bool) (Set, bool) {
	// Nothing to expand: no RRULE and no explicit RDATEs.
	if rule == "" && len(rDates) == 0 {
		return Set{}, false
	}
	var set *rrule.Set
	if rule != "" {
		var err error
		set, err = rrule.StrToRRuleSet("RRULE:" + rule)
		if err != nil {
			return Set{}, false
		}
	} else {
		// RDATE-only: start with an empty set; occurrences come from RDates.
		set = &rrule.Set{}
	}
	loc := TZForExpansion(tz)
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
		// instance shows forever — etag-gated sync never re-imports
		// an unchanged resource to repair them. Also exclude each EXDATE's
		// UTC wall clock reinterpreted in the expansion zone; for a healthy
		// EXDATE the reinterpreted instant matches no occurrence and is a
		// no-op. Date-only (all-day) EXDATEs are exact by construction and
		// midnight-UTC-tagged, so a reinterpretation would shift the day —
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
	// added to RDates so the DTSTART occurrence is not mislabelled IsOverride.
	if rule == "" {
		set.RDate(dtstart)
	}
	if dur < 0 {
		dur = 0
	}
	return Set{Rule: set, Loc: loc, RDates: buildRDateSet(rDates), Dur: dur}, true
}

// OccursAt reports whether the recurring set produces an occurrence whose
// instance key equals recurrenceID. An override whose RECURRENCE-ID is not
// a genuine occurrence of its master is an orphan. It is left behind when a
// series is truncated or split. It is not part of the recurrence set.
//
// The comparison uses the same instance key the suppression map keys on
// (InstanceTime.UTC() formatted as RFC 3339 vs the raw recurrence_id
// string). Suppression and orphan detection then cannot disagree about a
// given slot.
//
// The set must be built with includeExDates=false. A RECURRENCE-ID override
// wins over its slot. An EXDATE for the same slot must not make a
// legitimate override look like an orphan.
func (s Set) OccursAt(recurrenceID string) bool {
	t, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return false
	}
	want := t.UTC().Format(time.RFC3339)
	local := t
	if s.Loc != nil {
		local = t.In(s.Loc)
	}
	for _, occ := range s.Rule.Between(local.Add(-time.Second), local.Add(time.Second), true) {
		if occ.UTC().Format(time.RFC3339) == want {
			return true
		}
	}
	return false
}

// Between returns the occurrences in [from, to) whose [occ, occ+dur) interval
// overlaps the window, expanded once in the set's timezone. A multi-day instance
// whose start precedes 'from' can still overlap the window via its duration.
// Generation then begins one instance-duration early. Occurrences are kept by
// [start, end) overlap rather than start alone. Returned times are in the set's
// expansion zone. Callers normalize to UTC.
func (s Set) Between(from, to time.Time) []time.Time {
	localFrom, localTo := from, to
	if s.Loc != nil {
		localFrom = from.In(s.Loc)
		localTo = to.In(s.Loc)
	}
	occurrences := s.Rule.Between(localFrom.Add(-s.Dur), localTo, true)
	kept := occurrences[:0]
	for _, occ := range occurrences {
		if KeepOccurrence(occ, s.Dur, localFrom, localTo) {
			kept = append(kept, occ)
		}
	}
	return kept
}

// InWindow reports whether an occurrence at t falls within the half-open
// window [from, to). Recurrence ranges are half-open everywhere in chroncal.
func InWindow(t, from, to time.Time) bool {
	return !t.Before(from) && t.Before(to)
}

// OverlapsWindow reports whether the half-open interval [start, end) intersects
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
func OverlapsWindow(start, end, from, to time.Time) bool {
	if end.IsZero() {
		return InWindow(start, from, to)
	}
	return start.Before(to) && end.After(from)
}

// KeepOccurrence reports whether an expanded occurrence at occ with instance
// duration dur belongs in the half-open window [from, to). Its [occ, occ+dur)
// interval overlaps the window. For a zero-duration occurrence whose open
// end boundary OverlapsWindow would reject, occ itself falls inside it.
func KeepOccurrence(occ time.Time, dur time.Duration, from, to time.Time) bool {
	return OverlapsWindow(occ, occ.Add(dur), from, to) || InWindow(occ, from, to)
}

// Checker tests whether a RECURRENCE-ID names a genuine occurrence of its
// master. The master's RRULE is parsed once (EXDATEs ignored) and reused
// for all of the master's overrides. Anchor is the master's own occurrence
// instant. It is the sole occurrence when the master is non-recurring or
// its RRULE fails to parse. The zero Checker matches nothing.
type Checker struct {
	Set    Set
	Anchor time.Time
}

// NewChecker builds the Checker of a master: its RRULE and RDATEs anchored
// at dtstart in timezone tz, with the EXDATEs ignored.
func NewChecker(rule, tz string, dtstart time.Time, rDates []time.Time) Checker {
	s, _ := New(rule, tz, dtstart, 0, nil, rDates, false)
	return Checker{Set: s, Anchor: dtstart}
}

// OccursAt reports whether recurrenceID names an occurrence of the master.
func (c Checker) OccursAt(recurrenceID string) bool {
	if c.Set.Rule != nil {
		return c.Set.OccursAt(recurrenceID)
	}
	// Non-recurring / invalid RRULE master: the only occurrence is its anchor.
	t, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil || c.Anchor.IsZero() {
		return false
	}
	return c.Anchor.UTC().Format(time.RFC3339) == t.UTC().Format(time.RFC3339)
}

// RDateKey canonicalizes an RDATE/occurrence instant for membership lookups.
// The rrule iterator yields RDATE values truncated to whole seconds. A key
// on a second-granularity UTC RFC 3339 string then avoids sub-second
// precision or representation drift. A missed match would mislabel an
// explicitly-added occurrence as a plain RRULE instance. See issue #128.
func RDateKey(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// buildRDateSet returns a canonical-string set of RDATE instants for O(1)
// IsOverride membership checks. Keys are timezone-independent (RDateKey
// normalizes to UTC), so no location conversion is needed here.
func buildRDateSet(rdates []time.Time) map[string]struct{} {
	set := make(map[string]struct{}, len(rdates))
	for _, rd := range rdates {
		set[RDateKey(rd)] = struct{}{}
	}
	return set
}
