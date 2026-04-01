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

// ParseDateTime parses s as an RFC 3339 datetime.
// Returns zero time if s is empty.
func ParseDateTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	p, _ := time.Parse(time.RFC3339, s)
	return p
}
