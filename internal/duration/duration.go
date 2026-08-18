package duration

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// maxSeconds is the largest total, in seconds, that parse accepts. It is
// the number of whole seconds a time.Duration can hold (math.MaxInt64
// nanoseconds, about 292 years). Add multiplies hours, minutes, and
// seconds into a time.Duration. A total above this bound would wrap
// (issue #581). The bound also applies to the days and weeks components.
// Add moves days with AddDate, so days do not wrap the same way, but one
// consistent ceiling keeps the rule simple. For the bound, a day counts
// as 24 hours and a week counts as 7 days.
const maxSeconds = math.MaxInt64 / int64(time.Second)

type parsed struct {
	neg     bool
	weeks   int
	days    int
	hours   int
	minutes int
	seconds int
}

// checkRange rejects a duration whose total is more than maxSeconds.
// It guards each multiply before it runs, so the arithmetic itself
// cannot wrap. All components are non-negative here, so the running
// total stays far below math.MaxInt64 between checks.
func (p parsed) checkRange(orig string) error {
	var total int64
	for _, c := range []struct {
		value   int
		seconds int64
	}{
		{p.weeks, 7 * 86400},
		{p.days, 86400},
		{p.hours, 3600},
		{p.minutes, 60},
		{p.seconds, 1},
	} {
		v := int64(c.value)
		if v > maxSeconds/c.seconds {
			return fmt.Errorf("invalid duration %q: too large, the total is more than %d seconds", orig, maxSeconds)
		}
		total += v * c.seconds
		if total > maxSeconds {
			return fmt.Errorf("invalid duration %q: too large, the total is more than %d seconds", orig, maxSeconds)
		}
	}
	return nil
}

// consumeComponent extracts the unsigned integer before letter in s.
// If letter is absent, returns (s, 0, nil). Per RFC 5545 a component
// value is one or more DIGITs with no embedded sign. The whole-duration
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
		return p, p.checkRange(s)
	}

	r, p.days, err = consumeComponent(r, 'D', s)
	if err != nil {
		return parsed{}, err
	}

	if r == "" {
		return p, p.checkRange(s)
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
	return p, p.checkRange(s)
}

// FromGo converts a Go time.Duration to an RFC 5545 duration string.
// e.g. 1h30m → "PT1H30M", 90s → "PT1M30S", -15m → "-PT15M",
// 48h → "P2D", 168h → "P1W".
//
// Whole days are emitted as the date form (P#D). Exact whole weeks use
// the mutually-exclusive week form (P#W). Nominal multi-day spans then
// round-trip through Add. Add uses calendar-aware AddDate for days and
// weeks. An absolute "PT48H" would otherwise drift by an hour across a
// DST boundary.
//
// Sub-second precision is truncated toward zero. A Go duration carries
// nanoseconds. RFC 5545 durations have whole-second granularity.
// 1500ms becomes "PT1S". 500ms becomes "PT0S".
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
// Returns an error if the string is empty, malformed, or has leftover garbage.
// Also returns an error if the total is more than maxSeconds (about 292 years).
func Validate(s string) error {
	_, err := parse(s)
	return err
}

// Add parses an RFC 5545 duration string and adds it to a time.
// Format: [+/-]P[nW] or [+/-]P[nD][T[nH][nM][nS]]
// Returns zero time for empty, unparseable, or out-of-range input.
// Callers should validate with Validate() before they call Add().
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
