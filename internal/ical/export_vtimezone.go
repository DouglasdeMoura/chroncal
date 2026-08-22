package ical

import (
	"fmt"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// tzSpans accumulates, in first-seen order, the timezones an export references
// together with the inclusive [min, max] year span of the items that reference
// each. buildVTimezone anchors its DST rules on that span (issue #515). It
// does not use only the current year. An event dated in a different year
// then still resolves the right offset from the embedded VTIMEZONE. That
// year may be one whose zone observed a different DST rule.
type tzSpans struct {
	order    []string
	min, max map[string]int
}

// add records that an item in tzID falls in the given year. Empty and floating
// timezones carry no VTIMEZONE and are ignored.
func (s *tzSpans) add(tzID string, year int) {
	if tzID == "" || tzID == "FLOATING" {
		return
	}
	if s.min == nil {
		s.min = map[string]int{}
		s.max = map[string]int{}
	}
	if _, ok := s.min[tzID]; !ok {
		s.order = append(s.order, tzID)
		s.min[tzID], s.max[tzID] = year, year
		return
	}
	if year < s.min[tzID] {
		s.min[tzID] = year
	}
	if year > s.max[tzID] {
		s.max[tzID] = year
	}
}

// emit appends a VTIMEZONE child to cal for each referenced timezone, in
// first-seen order. Timezones Go cannot load are skipped in silence. This
// matches the prior best-effort behaviour.
func (s *tzSpans) emit(cal *ical.Calendar) {
	for _, tzID := range s.order {
		if vtz, err := buildVTimezone(tzID, s.min[tzID], s.max[tzID]); err == nil {
			cal.Children = append(cal.Children, vtz)
		}
	}
}

// recurrenceEndYear returns the calendar year of a recurring series' last
// occurrence. The VTIMEZONE span (issue #518) then covers every DST-rule era
// the series crosses, not only its start year.
//
// The span is capped at the end of the current year. A series bounded by a
// past UNTIL ends in its UNTIL year. An open-ended or COUNT-bounded series is
// clamped to today. DST-rule changes are historical. Future rule revisions
// are unknowable. Coverage of [start, today] is sufficient.
//
// The cap also keeps the rrule walk bounded. rrule-go reports a ~290-year
// sentinel UNTIL when a rule supplies none (issue #520). The cap must come
// from the walk, not from GetUntil(). The result is never earlier than the
// start year. A malformed rule degrades to the start year.
func recurrenceEndYear(rule string, start time.Time) int {
	startYear := start.Year()
	r, err := rrule.StrToRRule(rule)
	if err != nil {
		return startYear
	}
	r.DTStart(start)
	// Take the last occurrence on or before the end of the current year. Before()
	// walks only up to that cap (or the rule's UNTIL/COUNT limit, whichever comes
	// first), so iteration stays bounded for open-ended and COUNT-bounded rules
	// alike.
	capDate := time.Date(max(startYear, time.Now().Year())+1, 1, 1, 0, 0, 0, 0, time.UTC)
	if last := r.Before(capDate, true); !last.IsZero() {
		return max(startYear, last.Year())
	}
	return startYear
}

// buildVTimezone generates a VTIMEZONE component for the given IANA timezone ID.
// It covers the inclusive [fromYear, toYear] span of the items that reference
// it. It walks that span and finds STANDARD/DAYLIGHT offset transitions. It
// emits one observance per distinct DST rule period (RFC 5545 Section 3.6.5).
//
// When the zone's rule changed within the span, the superseded rule is
// bounded with UNTIL. Examples: the US 2007 DST extension, or a zone that
// abolished DST. A consumer that uses only the embedded VTIMEZONE then
// resolves the correct offset for every referenced year. It does not
// extrapolate the current year's rule (issue #515). A zero fromYear/toYear
// falls back to the current year.
func buildVTimezone(tzID string, fromYear, toYear int) (*ical.Component, error) {
	loc, err := time.LoadLocation(tzID)
	if err != nil {
		return nil, err
	}

	vtz := ical.NewComponent("VTIMEZONE")
	tzidProp := &ical.Prop{Name: "TZID"}
	tzidProp.Value = tzID
	vtz.Props.Set(tzidProp)

	if fromYear == 0 || toYear == 0 {
		fromYear = time.Now().Year()
		toYear = fromYear
	}
	if toYear < fromYear {
		fromYear, toYear = toYear, fromYear
	}

	fmtOffset := func(secs int) string {
		sign := "+"
		if secs < 0 {
			sign = "-"
			secs = -secs
		}
		return fmt.Sprintf("%s%02d%02d", sign, secs/3600, (secs%3600)/60)
	}

	// Walk the span month by month (sampling at noon to dodge the DST-hour
	// ambiguity), recording each offset transition with the exact instant it
	// takes effect.
	type transition struct {
		name       string
		offset     int       // seconds east of UTC at/after the transition
		fromOffset int       // seconds east of UTC before it
		instant    time.Time // exact UTC moment the new offset takes effect
	}

	start := time.Date(fromYear, 1, 1, 12, 0, 0, 0, loc)
	firstName, firstOffset := start.Zone()
	prevOffset := firstOffset

	var transitions []transition
	end := time.Date(toYear+1, 1, 1, 12, 0, 0, 0, loc)
	for cursor := start; cursor.Before(end); {
		next := cursor.AddDate(0, 1, 0)
		name, offset := next.Zone()
		if offset != prevOffset {
			transitions = append(transitions, transition{
				name:       name,
				offset:     offset,
				fromOffset: prevOffset,
				instant:    findTransitionInstant(cursor, next, prevOffset),
			})
			prevOffset = offset
		}
		cursor = next
	}

	addSubComp := func(compName, tzName string, offset, fromOffset int, dtstart time.Time, rrule string) {
		comp := ical.NewComponent(compName)

		// dtstart is the transition wall-clock already expressed in
		// TZOFFSETFROM (RFC 5545 Section 3.6.5), carried in a UTC-located
		// time.Time, so format its fields verbatim.
		p := &ical.Prop{Name: ical.PropDateTimeStart}
		p.Value = dtstart.Format("20060102T150405")
		comp.Props.Set(p)

		p = &ical.Prop{Name: "TZOFFSETFROM"}
		p.Value = fmtOffset(fromOffset)
		comp.Props.Set(p)

		p = &ical.Prop{Name: "TZOFFSETTO"}
		p.Value = fmtOffset(offset)
		comp.Props.Set(p)

		p = &ical.Prop{Name: "TZNAME"}
		p.Value = tzName
		comp.Props.Set(p)

		if rrule != "" {
			p = &ical.Prop{Name: ical.PropRecurrenceRule}
			p.Value = rrule
			comp.Props.Set(p)
		}

		vtz.Children = append(vtz.Children, comp)
	}

	if len(transitions) == 0 {
		// No DST anywhere in the span — a single STANDARD observance.
		addSubComp("STANDARD", firstName, firstOffset, firstOffset,
			time.Date(fromYear, 1, 1, 0, 0, 0, 0, time.UTC), "")
		return vtz, nil
	}

	// Group transitions into observances, one per distinct DST rule. A yearly
	// RRULE collapses repeats of the same rule across years; when a
	// STANDARD/DAYLIGHT rule is later superseded by a different one, bound the
	// older observance with UNTIL (the UTC instant of its final occurrence) so
	// both rules don't fire in the years after the change.
	type observance struct {
		compName, tzName   string
		offset, fromOffset int
		dtstart            time.Time // wall-clock in fromOffset
		rrule, sig         string
		lastSeen           time.Time // UTC instant of its most recent occurrence
		until              time.Time // zero = open-ended
	}

	var observances []*observance
	current := map[string]*observance{} // open observance per component kind

	for _, tr := range transitions {
		compName := "STANDARD"
		if tr.offset > tr.fromOffset {
			compName = "DAYLIGHT"
		}
		fromWall := tr.instant.UTC().Add(time.Duration(tr.fromOffset) * time.Second)
		rrule := transitionRRULE(fromWall)
		sig := compName + "|" + rrule + "|" + fmtOffset(tr.offset) + "|" + fmtOffset(tr.fromOffset)

		if cur := current[compName]; cur != nil && cur.sig == sig {
			cur.lastSeen = tr.instant // same rule continues; RRULE covers it
			continue
		}
		if cur := current[compName]; cur != nil {
			cur.until = cur.lastSeen // rule changed; cap the prior one
		}
		obs := &observance{
			compName: compName, tzName: tr.name,
			offset: tr.offset, fromOffset: tr.fromOffset,
			dtstart: fromWall, rrule: rrule, sig: sig, lastSeen: tr.instant,
		}
		observances = append(observances, obs)
		current[compName] = obs
	}

	// If the zone no longer observes DST by the end of the span (it was
	// abolished, e.g. Brazil in 2019), the final year carries no transitions, so
	// the trailing rules would otherwise recur forever and resolve a spurious
	// offset for later occurrences. Cap every still-open observance at its final
	// occurrence; the latest-onset observance then governs all later times with
	// the correct standing offset. A zone that still observes DST has two
	// transitions in its final year, so its trailing rules stay open-ended.
	finalYearTransitions := 0
	for _, tr := range transitions {
		if tr.instant.UTC().Year() == toYear {
			finalYearTransitions++
		}
	}
	if finalYearTransitions < 2 {
		for _, obs := range observances {
			if obs.until.IsZero() {
				obs.until = obs.lastSeen
			}
		}
	}

	for _, obs := range observances {
		rrule := obs.rrule
		if !obs.until.IsZero() {
			rrule += ";UNTIL=" + obs.until.UTC().Format("20060102T150405Z")
		}
		addSubComp(obs.compName, obs.tzName, obs.offset, obs.fromOffset, obs.dtstart, rrule)
	}

	return vtz, nil
}

// findTransitionInstant binary-searches (lo, hi] for the exact instant the UTC
// offset changes away from prevOffset, to one-second precision. Callers pass
// bounds known to bracket exactly one transition: offset(lo) == prevOffset and
// offset(hi) != prevOffset. The returned instant is the first second that
// carries the new offset. That is the precise transition moment, regardless of
// the local hour at which it occurs.
func findTransitionInstant(lo, hi time.Time, prevOffset int) time.Time {
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		if _, offset := mid.Zone(); offset == prevOffset {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

// transitionRRULE builds a yearly RFC 5545 recurrence rule for when a
// DST transition repeats. It is derived from the weekday-of-month of dtstart.
// Most IANA zones transition on a fixed ordinal weekday (for example
// "2nd Sunday of March" -> FREQ=YEARLY;BYMONTH=3;BYDAY=2SU). When the weekday
// is the last such weekday of the month, BYDAY uses -1 (for example last
// Sunday -> BYDAY=-1SU). That also matches the common European rule.
func transitionRRULE(dtstart time.Time) string {
	weekdays := [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	wd := weekdays[dtstart.Weekday()]
	month := int(dtstart.Month())
	// Last occurrence of this weekday in the month? (One week later spills
	// into the next month.)
	if dtstart.AddDate(0, 0, 7).Month() != dtstart.Month() {
		return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=-1%s", month, wd)
	}
	nth := (dtstart.Day()-1)/7 + 1
	return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", month, nth, wd)
}
