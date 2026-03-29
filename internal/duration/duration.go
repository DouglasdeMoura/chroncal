package duration

import (
	"fmt"
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

// Validate checks that s is a well-formed RFC 5545 duration string.
// Format: [+/-]P[nW] or [+/-]P[nD][T[nH][nM][nS]]
// Returns an error if the string is empty, malformed, or has trailing garbage.
func Validate(s string) error {
	if s == "" {
		return fmt.Errorf("duration must not be empty")
	}

	r := s
	if r[0] == '-' || r[0] == '+' {
		r = r[1:]
	}

	if len(r) == 0 || r[0] != 'P' {
		return fmt.Errorf("invalid duration %q: must start with P", s)
	}
	r = r[1:] // strip P

	if r == "" {
		return fmt.Errorf("invalid duration %q: no components after P", s)
	}

	// Week form: nW (mutually exclusive with other components)
	if i := strings.Index(r, "W"); i >= 0 {
		if i == 0 {
			return fmt.Errorf("invalid duration %q: W requires a number", s)
		}
		if _, err := strconv.Atoi(r[:i]); err != nil {
			return fmt.Errorf("invalid duration %q: bad week count %q", s, r[:i])
		}
		if r[i+1:] != "" {
			return fmt.Errorf("invalid duration %q: trailing characters after W", s)
		}
		return nil
	}

	// Date part: optional nD
	if i := strings.Index(r, "D"); i >= 0 {
		if i == 0 {
			return fmt.Errorf("invalid duration %q: D requires a number", s)
		}
		if _, err := strconv.Atoi(r[:i]); err != nil {
			return fmt.Errorf("invalid duration %q: bad day count %q", s, r[:i])
		}
		r = r[i+1:]
	}

	if r == "" {
		return nil // Just date components, e.g. P1D
	}

	// Time part: T followed by nH, nM, nS
	if r[0] != 'T' {
		return fmt.Errorf("invalid duration %q: unexpected character %q", s, string(r[0]))
	}
	r = r[1:]

	if r == "" {
		return fmt.Errorf("invalid duration %q: T requires at least one time component", s)
	}

	if i := strings.Index(r, "H"); i >= 0 {
		if i == 0 {
			return fmt.Errorf("invalid duration %q: H requires a number", s)
		}
		if _, err := strconv.Atoi(r[:i]); err != nil {
			return fmt.Errorf("invalid duration %q: bad hour count %q", s, r[:i])
		}
		r = r[i+1:]
	}
	if i := strings.Index(r, "M"); i >= 0 {
		if i == 0 {
			return fmt.Errorf("invalid duration %q: M requires a number", s)
		}
		if _, err := strconv.Atoi(r[:i]); err != nil {
			return fmt.Errorf("invalid duration %q: bad minute count %q", s, r[:i])
		}
		r = r[i+1:]
	}
	if i := strings.Index(r, "S"); i >= 0 {
		if i == 0 {
			return fmt.Errorf("invalid duration %q: S requires a number", s)
		}
		if _, err := strconv.Atoi(r[:i]); err != nil {
			return fmt.Errorf("invalid duration %q: bad second count %q", s, r[:i])
		}
		r = r[i+1:]
	}

	if r != "" {
		return fmt.Errorf("invalid duration %q: trailing characters %q", s, r)
	}
	return nil
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
