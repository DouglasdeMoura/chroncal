package duration

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type parsed struct {
	neg     bool
	weeks   int
	days    int
	hours   int
	minutes int
	seconds int
}

// consumeComponent extracts the integer preceding letter in s.
// If letter is absent, returns (s, 0, nil).
func consumeComponent(s string, letter byte, orig string) (string, int, error) {
	i := strings.IndexByte(s, letter)
	if i < 0 {
		return s, 0, nil
	}
	if i == 0 {
		return "", 0, fmt.Errorf("invalid duration %q: %c requires a number", orig, letter)
	}
	v, err := strconv.Atoi(s[:i])
	if err != nil {
		return "", 0, fmt.Errorf("invalid duration %q: bad %c value %q", orig, letter, s[:i])
	}
	return s[i+1:], v, nil
}

// parse parses an RFC 5545 duration string into its components.
// Format: [+/-]P[nW] or [+/-]P[nD][T[nH][nM][nS]]
func parse(s string) (parsed, error) {
	if s == "" {
		return parsed{}, fmt.Errorf("duration must not be empty")
	}

	r := s
	var p parsed
	switch r[0] {
	case '-':
		p.neg = true
		r = r[1:]
	case '+':
		r = r[1:]
	}

	if len(r) == 0 || r[0] != 'P' {
		return parsed{}, fmt.Errorf("invalid duration %q: must start with P", s)
	}
	r = r[1:]

	if r == "" {
		return parsed{}, fmt.Errorf("invalid duration %q: no components after P", s)
	}

	var err error

	// Week form (mutually exclusive with other components)
	if strings.IndexByte(r, 'W') >= 0 {
		r, p.weeks, err = consumeComponent(r, 'W', s)
		if err != nil {
			return parsed{}, err
		}
		if r != "" {
			return parsed{}, fmt.Errorf("invalid duration %q: trailing characters after W", s)
		}
		return p, nil
	}

	r, p.days, err = consumeComponent(r, 'D', s)
	if err != nil {
		return parsed{}, err
	}

	if r == "" {
		return p, nil
	}

	if r[0] != 'T' {
		return parsed{}, fmt.Errorf("invalid duration %q: unexpected character %c", s, r[0])
	}
	r = r[1:]

	if r == "" {
		return parsed{}, fmt.Errorf("invalid duration %q: T requires at least one time component", s)
	}

	r, p.hours, err = consumeComponent(r, 'H', s)
	if err != nil {
		return parsed{}, err
	}
	r, p.minutes, err = consumeComponent(r, 'M', s)
	if err != nil {
		return parsed{}, err
	}
	r, p.seconds, err = consumeComponent(r, 'S', s)
	if err != nil {
		return parsed{}, err
	}

	if r != "" {
		return parsed{}, fmt.Errorf("invalid duration %q: trailing characters %q", s, r)
	}
	return p, nil
}

// FromGo converts a Go time.Duration to an RFC 5545 duration string.
// e.g. 1h30m → "PT1H30M", 90s → "PT1M30S", -15m → "-PT15M".
func FromGo(d time.Duration) string {
	if d == 0 {
		return "PT0S"
	}
	var b strings.Builder
	neg := d < 0
	if neg {
		b.WriteByte('-')
		d = -d
	}
	b.WriteByte('P')
	total := int(d / time.Second)
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
	_, err := parse(s)
	return err
}

// Add parses an RFC 5545 duration string and adds it to a time.
// Format: [+/-]P[nW] or [+/-]P[nD][T[nH][nM][nS]]
// Returns zero time for empty or unparseable input. Callers should
// validate with Validate() before calling Add().
func Add(t time.Time, dur string) time.Time {
	p, err := parse(dur)
	if err != nil {
		return time.Time{}
	}

	days := p.days + p.weeks*7
	d := time.Duration(p.hours)*time.Hour +
		time.Duration(p.minutes)*time.Minute +
		time.Duration(p.seconds)*time.Second

	if p.neg {
		return t.AddDate(0, 0, -days).Add(-d)
	}
	return t.AddDate(0, 0, days).Add(d)
}
