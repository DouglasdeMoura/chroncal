package timeutil

import (
	"strings"
	"time"
)

// dateOnlyLoc tags date-only (all-day) values parsed by ParseTimeList and
// ParseRecurrenceID. It has a zero offset, so the resulting instant is exactly
// midnight UTC — consistent with how all-day events are stored (issue #64) and
// with EXDATE/RDATE matching, which is purely instant-based. Its distinct
// identity (it is not time.UTC) lets SerializeTimeList re-emit these values as
// date-only "YYYY-MM-DD" strings — and therefore as iCal VALUE=DATE — without
// misclassifying a timed occurrence that merely happens to fall on midnight
// UTC, which must round-trip as a full RFC 3339 DATE-TIME.
var dateOnlyLoc = time.FixedZone("DATE", 0)

// AsDateOnly returns t's calendar date re-tagged with the all-day marker
// location (dateOnlyLoc), so SerializeTimeList emits it as a date-only
// "YYYY-MM-DD" value — i.e. an iCal VALUE=DATE matching DTSTART;VALUE=DATE for
// all-day events (RFC 5545 §3.8.5.1). It uses t's UTC calendar day, matching
// how all-day occurrences are stored (midnight UTC).
func AsDateOnly(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, dateOnlyLoc)
}

// StorageTimeFormat is the canonical layout for UTC timestamps written to the
// database (e.g. deleted_at cutoffs). It is RFC 3339 without the explicit
// numeric offset, since all stored times are UTC.
const StorageTimeFormat = "2006-01-02T15:04:05Z"

// IsDateOnly returns true if s is a date-only string in YYYY-MM-DD format.
func IsDateOnly(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// LocalDay returns midnight at the start of t's local calendar day, in the
// local location. Unlike t.Truncate(24*time.Hour) — which floors the absolute
// instant to a multiple of 24h and therefore always aligns to UTC midnight
// regardless of any preceding .Local() — this computes the day boundary from
// the local Year/Month/Day, so two instants on the same local calendar day map
// to the same value even when they straddle UTC midnight.
func LocalDay(t time.Time) time.Time {
	l := t.Local()
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.Local)
}

// ParseDate parses s as either a date-only string (YYYY-MM-DD) or an RFC 3339 datetime.
// Returns zero time if s is empty.
func ParseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if p, err := time.Parse("2006-01-02", s); err == nil {
		return p
	}
	p, _ := time.Parse(time.RFC3339, s)
	return p
}

// ParseRecurrenceID parses a recurrence ID string in RFC 3339 or date-only
// (2006-01-02) format for all-day events. Date-only IDs resolve to midnight
// UTC (via dateOnlyLoc), matching how all-day occurrences are stored (import
// records VALUE=DATE as midnight UTC) so the ID compares equal to the
// occurrence it identifies. In practice sync and import always normalise
// recurrence IDs to full UTC RFC 3339, so the date-only branch is a defensive
// fallback.
func ParseRecurrenceID(id string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, id); err == nil {
		return t, nil
	}
	t, err := time.Parse("2006-01-02", id)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, dateOnlyLoc), nil
}

// ParseDateTime parses s as an RFC 3339 datetime.
// Returns zero time if s is empty.
func ParseDateTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	p, _ := time.Parse(time.RFC3339, s)
	return p
}

// ParseTimeList parses a comma-separated string of RFC 3339 or date-only
// ("2006-01-02") timestamps into a slice of time.Time.
func ParseTimeList(s string) []time.Time {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]time.Time, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if t, err := time.Parse(time.RFC3339, p); err == nil {
			out = append(out, t)
		} else if t, err := time.Parse("2006-01-02", p); err == nil {
			// Parse date-only into midnight UTC (via dateOnlyLoc) to match how
			// all-day events are stored (import.go records VALUE=DATE as
			// midnight UTC). Using time.Local here would shift the instant by
			// the host offset, so an all-day EXDATE/RDATE would land on the
			// wrong UTC day and fail to match (or mis-add) the occurrence on
			// non-UTC hosts. dateOnlyLoc keeps the all-day signal for
			// SerializeTimeList without affecting the (instant-based) matching.
			out = append(out, time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, dateOnlyLoc))
		}
	}
	return out
}

// SerializeTimeList is the inverse of ParseTimeList. It formats a value as
// date-only ("2006-01-02") only when it carries the all-day marker
// (dateOnlyLoc, set by ParseTimeList for VALUE=DATE values); every other value
// — including a timed occurrence that happens to fall on midnight UTC — is
// formatted as full RFC 3339 so it round-trips as an iCal DATE-TIME.
func SerializeTimeList(times []time.Time) string {
	if len(times) == 0 {
		return ""
	}
	parts := make([]string, len(times))
	for i, t := range times {
		if t.Location() == dateOnlyLoc {
			parts[i] = t.Format("2006-01-02")
		} else {
			parts[i] = t.UTC().Format(time.RFC3339)
		}
	}
	return strings.Join(parts, ",")
}

// JoinCategoryList joins category values into a single comma-separated string,
// backslash-escaping any backslash or comma inside an individual value so the
// result is an exact inverse of ParseCategoryList. This keeps a category that
// legitimately contains a comma (e.g. "Foo, Bar") as one value across the
// in-memory comma-joined representation. Empty/whitespace-only values are
// dropped.
func JoinCategoryList(cats []string) string {
	parts := make([]string, 0, len(cats))
	for _, c := range cats {
		if c = strings.TrimSpace(c); c != "" {
			parts = append(parts, escapeCategory(c))
		}
	}
	return strings.Join(parts, ",")
}

func escapeCategory(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ",", `\,`)
	return s
}

// ParseCategoryList splits a comma-separated string produced by
// JoinCategoryList into trimmed non-empty values. Commas escaped as "\," are
// treated as part of a value rather than separators, and "\\" decodes to a
// single backslash.
func ParseCategoryList(s string) []string {
	if s == "" {
		return nil
	}
	var (
		out     []string
		b       strings.Builder
		escaped bool
	)
	flush := func() {
		if v := strings.TrimSpace(b.String()); v != "" {
			out = append(out, v)
		}
		b.Reset()
	}
	for _, r := range s {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == ',':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		// Trailing lone backslash: preserve it literally.
		b.WriteByte('\\')
	}
	flush()
	return out
}
