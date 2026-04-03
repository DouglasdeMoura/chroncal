package timeutil

import (
	"strings"
	"time"
)

// IsDateOnly returns true if s is a date-only string in YYYY-MM-DD format.
func IsDateOnly(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
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
// (2006-01-02) format for all-day events.
func ParseRecurrenceID(id string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, id); err == nil {
		return t, nil
	}
	t, err := time.Parse("2006-01-02", id)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local), nil
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
			// Parse date-only into time.Local to match how all-day events
			// are stored (import.go uses time.Local for VALUE=DATE).
			out = append(out, time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local))
		}
	}
	return out
}

// SerializeTimeList is the inverse of ParseTimeList. It formats each time as
// date-only ("2006-01-02") when the time component is midnight in time.Local
// (matching all-day event EXDATEs), or RFC 3339 otherwise.
func SerializeTimeList(times []time.Time) string {
	if len(times) == 0 {
		return ""
	}
	parts := make([]string, len(times))
	for i, t := range times {
		local := t.In(time.Local)
		if local.Hour() == 0 && local.Minute() == 0 && local.Second() == 0 && local.Nanosecond() == 0 &&
			t.Location() == time.Local {
			parts[i] = t.Format("2006-01-02")
		} else {
			parts[i] = t.UTC().Format(time.RFC3339)
		}
	}
	return strings.Join(parts, ",")
}

// ParseCategoryList splits a comma-separated string into trimmed non-empty values.
func ParseCategoryList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
