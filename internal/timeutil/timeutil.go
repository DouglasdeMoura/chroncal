package timeutil

import "time"

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
