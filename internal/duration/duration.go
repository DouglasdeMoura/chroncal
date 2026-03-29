package duration

import (
	"strconv"
	"strings"
	"time"
)

// FromGo converts a Go time.Duration to an RFC 5545 duration string.
// e.g. 1h30m → "PT1H30M", 90s → "PT1M30S".
func FromGo(d time.Duration) string {
	if d == 0 {
		return "PT0S"
	}
	var b strings.Builder
	b.WriteByte('P')
	total := int(d.Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 || m > 0 || s > 0 {
		b.WriteByte('T')
		if h > 0 {
			b.WriteString(strconv.Itoa(h))
			b.WriteByte('H')
		}
		if m > 0 {
			b.WriteString(strconv.Itoa(m))
			b.WriteByte('M')
		}
		if s > 0 {
			b.WriteString(strconv.Itoa(s))
			b.WriteByte('S')
		}
	}
	return b.String()
}

// Add parses an RFC 5545 duration string and adds it to a time.
// Format: [+/-]P[nW] or [+/-]P[nD][T[nH][nM][nS]]
// An empty string defaults to +1 hour.
func Add(t time.Time, dur string) time.Time {
	if dur == "" {
		return t.Add(time.Hour)
	}

	s := dur
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	} else if s[0] == '+' {
		s = s[1:]
	}

	if len(s) == 0 || s[0] != 'P' {
		return t.Add(time.Hour)
	}
	s = s[1:]

	var d time.Duration
	var days int

	if i := strings.Index(s, "W"); i >= 0 {
		if w, err := strconv.Atoi(s[:i]); err == nil {
			days = w * 7
		}
		if neg {
			return t.AddDate(0, 0, -days)
		}
		return t.AddDate(0, 0, days)
	}

	if i := strings.Index(s, "D"); i >= 0 {
		if v, err := strconv.Atoi(s[:i]); err == nil {
			days = v
		}
		s = s[i+1:]
	}

	if len(s) > 0 && s[0] == 'T' {
		s = s[1:]
		if i := strings.Index(s, "H"); i >= 0 {
			if v, err := strconv.Atoi(s[:i]); err == nil {
				d += time.Duration(v) * time.Hour
			}
			s = s[i+1:]
		}
		if i := strings.Index(s, "M"); i >= 0 {
			if v, err := strconv.Atoi(s[:i]); err == nil {
				d += time.Duration(v) * time.Minute
			}
			s = s[i+1:]
		}
		if i := strings.Index(s, "S"); i >= 0 {
			if v, err := strconv.Atoi(s[:i]); err == nil {
				d += time.Duration(v) * time.Second
			}
		}
	}

	if neg {
		return t.AddDate(0, 0, -days).Add(-d)
	}
	return t.AddDate(0, 0, days).Add(d)
}
