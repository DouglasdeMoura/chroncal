// Package sharefreebusy builds free times from busy times.
package sharefreebusy

import (
	"sort"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/freebusy"
)

// Free marks a free period type.
const Free = "FREE"

// Invert returns the free gaps of the busy periods.
// It clamps each busy period to the window.
// It merges overlaps and sorts the gaps.
// An empty list gives the full window as one free period.
// It returns nil for an empty or reverse window.
func Invert(busy []freebusy.Period, from, to time.Time) []freebusy.Period {
	if !to.After(from) {
		return nil
	}
	from = from.UTC()
	to = to.UTC()

	clamped := make([]freebusy.Period, 0, len(busy))
	for _, p := range busy {
		start := p.Start.UTC()
		end := p.End.UTC()
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			continue
		}
		clamped = append(clamped, freebusy.Period{Start: start, End: end, Type: freebusy.Busy})
	}

	sort.Slice(clamped, func(i, j int) bool {
		if clamped[i].Start.Equal(clamped[j].Start) {
			return clamped[i].End.Before(clamped[j].End)
		}
		return clamped[i].Start.Before(clamped[j].Start)
	})

	merged := make([]freebusy.Period, 0, len(clamped))
	for _, p := range clamped {
		if len(merged) == 0 {
			merged = append(merged, p)
			continue
		}
		last := &merged[len(merged)-1]
		if !p.Start.After(last.End) {
			if p.End.After(last.End) {
				last.End = p.End
			}
			continue
		}
		merged = append(merged, p)
	}

	var free []freebusy.Period
	cursor := from
	for _, m := range merged {
		if cursor.Before(m.Start) {
			free = append(free, freebusy.Period{Start: cursor, End: m.Start, Type: Free})
		}
		if m.End.After(cursor) {
			cursor = m.End
		}
	}
	if cursor.Before(to) {
		free = append(free, freebusy.Period{Start: cursor, End: to, Type: Free})
	}
	return free
}

// CapRange limits the window to 31 days.
// It clamps To when the window exceeds 31 days.
// It reports capped true in that case.
// It reports capped false for a window within the limit.
// It reports capped false when To does not exceed From.
func CapRange(from, to time.Time) (time.Time, time.Time, bool) {
	if !to.After(from) {
		return from, to, false
	}
	limit := from.AddDate(0, 0, 31)
	if to.After(limit) {
		return from, limit, true
	}
	return from, to, false
}

// ClampWorkHours keeps free times in work hours.
// Work hours are 09:00 to 17:00 local each day.
// It returns free periods in UTC.
func ClampWorkHours(free []freebusy.Period) []freebusy.Period {
	out := make([]freebusy.Period, 0, len(free))
	for _, p := range free {
		for day := startDayLocal(p.Start); day.Before(p.End); day = day.AddDate(0, 0, 1) {
			ws := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, time.Local)
			we := time.Date(day.Year(), day.Month(), day.Day(), 17, 0, 0, 0, time.Local)
			s := maxTime(p.Start, ws)
			e := minTime(p.End, we)
			if e.After(s) {
				out = append(out, freebusy.Period{Start: s.UTC(), End: e.UTC(), Type: Free})
			}
		}
	}
	sortFree(out)
	return out
}

func startDayLocal(t time.Time) time.Time {
	l := t.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.Local)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func sortFree(p []freebusy.Period) {
	sort.Slice(p, func(i, j int) bool { return p[i].Start.Before(p[j].Start) })
}

// FormatText forms one line for each free period.
// Each line holds start, a space, and end in RFC 3339.
// It returns "no free periods" plus a newline for no period.
func FormatText(free []freebusy.Period) string {
	if len(free) == 0 {
		return "no free periods\n"
	}
	var b strings.Builder
	for _, p := range free {
		b.WriteString(p.Start.UTC().Format(time.RFC3339))
		b.WriteString(" ")
		b.WriteString(p.End.UTC().Format(time.RFC3339))
		b.WriteString("\n")
	}
	return b.String()
}
