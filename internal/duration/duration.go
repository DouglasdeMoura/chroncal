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

// consumeComponent extracts the unsigned integer preceding letter in s.
// If letter is absent, returns (s, 0, nil). Per RFC 5545 a component
// value is one or more DIGITs with no embedded sign; the whole-duration
// sign is handled once in parse, so "PT-1H" and "PT+1H" are rejected.
func consumeComponent(s string, letter byte, orig string) (string, int, error) {
	i := strings.IndexByte(s, letter)
	if i < 0 {
		return s, 0, nil
	}
	if i == 0 {
		return "", 0, fmt.Errorf("invalid duration %q: %c requires a number", orig, letter)
	}
	num := s[:i]
	// strconv.Atoi rejects every non-digit except a leading sign, so a
	// first-byte sign check is all that's needed to forbid per-component
	// signs (num is non-empty here because i > 0).
	if num[0] == '+' || num[0] == '-' {
		return "", 0, fmt.Errorf("invalid duration %q: bad %c value %q", orig, letter, num)
	}
	v, err := strconv.Atoi(num)
	if err != nil {
		return "", 0, fmt.Errorf("invalid duration %q: bad %c value %q", orig, letter, num)
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
// e.g. 1h30m → "PT1H30M", 90s → "PT1M30S", -15m → "-PT15M",
// 48h → "P2D", 168h → "P1W".
//
// Whole days are emitted as the date form (P#D) and exact whole weeks as
// the mutually-exclusive week form (P#W) so that nominal multi-day spans
// round-trip through Add, which uses calendar-aware AddDate for days and
// weeks. An absolute "PT48H" would otherwise drift by an hour across a
// DST boundary.
//
// Sub-second precision is truncated toward zero: a Go duration carries
// nanoseconds but RFC 5545 durations have whole-second granularity, so
// 1500ms becomes "PT1S" and 500ms becomes "PT0S".
func FromGo(d time.Duration) string {
	total := int64(d / time.Second)
	if total == 0 {
		return "PT0S"
	}
	var b strings.Builder
	if total < 0 {
		b.WriteByte('-')
		total = -total
	}
	b.WriteByte('P')

	const secsPerDay = 86400
	// Exact whole weeks use the week form, which RFC 5545 makes
	// mutually exclusive with all other components.
	if total%(7*secsPerDay) == 0 {
		b.WriteString(strconv.FormatInt(total/(7*secsPerDay), 10))
		b.WriteByte('W')
		return b.String()
	}

	days := total / secsPerDay
	rem := total % secsPerDay
	h := rem / 3600
	m := (rem % 3600) / 60
	s := rem % 60
	if days > 0 {
		b.WriteString(strconv.FormatInt(days, 10))
		b.WriteByte('D')
	}
	if h > 0 || m > 0 || s > 0 {
		b.WriteByte('T')
		if h > 0 {
			b.WriteString(strconv.FormatInt(h, 10))
			b.WriteByte('H')
		}
		if m > 0 {
			b.WriteString(strconv.FormatInt(m, 10))
			b.WriteByte('M')
		}
		if s > 0 {
			b.WriteString(strconv.FormatInt(s, 10))
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
