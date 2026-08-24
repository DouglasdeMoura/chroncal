package recurrence

import (
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// instanceScopeHorizon bounds the forward search behind HasOccurrenceFrom.
// Rules sparser than this are rejected as a no-op instead of silently
// truncating nothing. Five years covers every sane interval; a longer gap is
// indistinguishable from an exhausted rule.
const instanceScopeHorizon = 5 * 365 * 24 * time.Hour

// OccurrenceExistsAt reports whether evt's raw rule set generates an instance
// at at. EXDATEs apply, overrides do not hide their slot: deleting an instance
// targets its original RECURRENCE-ID even when an override moved it elsewhere.
// A cancelled series generates nothing. A non-recurring or unparseable master
// matches only its own start.
func OccurrenceExistsAt(evt event.Event, at time.Time) bool {
	if cancelledRecurringMaster(evt.RecurrenceRule, evt.Status) {
		return false
	}
	rs, ok := newEventRRuleSet(evt, true)
	if !ok {
		return evt.StartTime.Equal(at)
	}
	return rs.occursAt(at.UTC().Format(time.RFC3339))
}

// HasOccurrenceFrom reports whether scoping the series at from removes at
// least one generated instance. Same override and cancellation rules as
// OccurrenceExistsAt.
func HasOccurrenceFrom(evt event.Event, from time.Time) bool {
	if cancelledRecurringMaster(evt.RecurrenceRule, evt.Status) {
		return false
	}
	rs, ok := newEventRRuleSet(evt, true)
	if !ok {
		return !evt.StartTime.Before(from)
	}
	// rs.between keeps occurrences that merely overlap the window (a
	// multi-day instance started earlier still runs at from). Deletion keys
	// on starts, so only a start at or after the cutoff counts.
	lo := from.UTC()
	for _, occ := range rs.between(lo, lo.Add(instanceScopeHorizon)) {
		if !occ.Before(lo) {
			return true
		}
	}
	return false
}

// NextOccurrenceAfter returns the first instance strictly after after, or
// ok=false when none exists within the scan horizon. Callers use it to point
// at the nearest valid slot in rejection messages.
func NextOccurrenceAfter(evt event.Event, after time.Time) (time.Time, bool) {
	if cancelledRecurringMaster(evt.RecurrenceRule, evt.Status) {
		return time.Time{}, false
	}
	rs, ok := newEventRRuleSet(evt, true)
	if !ok {
		if evt.StartTime.After(after) {
			return evt.StartTime, true
		}
		return time.Time{}, false
	}
	lo := after.UTC()
	for _, occ := range rs.between(lo, lo.Add(instanceScopeHorizon)) {
		if occ.After(after) {
			return occ, true
		}
	}
	return time.Time{}, false
}

// IsCancelledSeries mirrors cancelledRecurringMaster for callers that want to
// name the condition instead of a generic no-occurrence error.
func IsCancelledSeries(evt event.Event) bool {
	return cancelledRecurringMaster(evt.RecurrenceRule, evt.Status)
}

// ScopeInstanceTime returns the deletion key for one instance of ev: the
// original RECURRENCE-ID for an override, else its start. DeleteInstance and
// DeleteFromInstance match overrides by that stored id, never by the moved
// display time.
func ScopeInstanceTime(ev event.Event) (time.Time, error) {
	if ev.RecurrenceID != "" {
		return timeutil.ParseRecurrenceID(ev.RecurrenceID)
	}
	return ev.StartTime, nil
}
