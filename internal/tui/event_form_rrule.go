package tui

import (
	"strings"
	"time"
)

// parseRecurrenceRule matches a recurrence rule against the form presets and
// extracts ending conditions.
func parseRecurrenceRule(rule string, fallbackDate time.Time) (int, string, endsMode, time.Time) {
	var ends endsMode
	endsDate := fallbackDate.AddDate(0, 1, 0)

	parts := strings.Split(rule, ";")
	var baseParts []string
	for _, p := range parts {
		upper := strings.ToUpper(p)
		switch {
		case strings.HasPrefix(upper, "COUNT="):
			ends = endsAfter
		case strings.HasPrefix(upper, "UNTIL="):
			ends = endsOnDate
			if t, ok := parseRRuleUntil(strings.TrimPrefix(upper, "UNTIL=")); ok {
				endsDate = t
			}
		default:
			baseParts = append(baseParts, p)
		}
	}
	base := strings.Join(baseParts, ";")

	for i := 1; i < len(repeatPresets); i++ {
		if i == repeatCustomIdx {
			continue
		}
		if strings.EqualFold(base, repeatPresets[i].Rule) {
			return i, "", ends, endsDate
		}
	}

	return repeatCustomIdx, rule, ends, endsDate
}

// parseRRuleUntil parses an RRULE UNTIL value as either a UTC datetime
// (20060102T150405Z) or a date-only value (20060102). It returns ok=false when
// neither layout matches. The result is expressed in the local zone. The
// end-of-day anchor written by formatRRuleUntil then maps back to the calendar
// day the user picked (see issue #146).
func parseRRuleUntil(val string) (time.Time, bool) {
	if t, err := time.Parse("20060102T150405Z", val); err == nil {
		return t.Local(), true
	}
	if t, err := time.Parse("20060102", val); err == nil {
		// A date-only UNTIL is a floating calendar date, not an absolute
		// instant; reinterpret the wall-clock date in the local zone so it is
		// not shifted a day by the parser's UTC default.
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local), true
	}
	return time.Time{}, false
}

// formatRRuleUntil renders a time as an RRULE UNTIL value in iCal UTC form.
//
// The ends-date arrives from the date picker as local midnight, i.e. the start
// of the chosen end day. UNTIL is inclusive. An anchor there would exclude
// every same-day occurrence. They fire at the event's start time, later than
// midnight. For a positive UTC offset, the UTC value would even roll back
// to the previous calendar day. Anchor to the final second of that local day
// before a convert to UTC. The chosen end day is then always covered.
func formatRRuleUntil(t time.Time) string {
	y, mo, d := t.Date()
	endOfDay := time.Date(y, mo, d, 23, 59, 59, 0, t.Location())
	return endOfDay.UTC().Format("20060102T150405Z")
}

func rruleParam(rule, name string) string {
	for p := range strings.SplitSeq(rule, ";") {
		if k, v, ok := strings.Cut(p, "="); ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
