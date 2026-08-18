package duration

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// maxSeconds is the largest time total, in seconds, that parse accepts
// for the hours, minutes, and seconds components. It is the number of
// whole seconds a time.Duration can hold (math.MaxInt64 nanoseconds,
// about 292 years). Add multiplies these components into a
// time.Duration. A total above this bound would wrap (issue #581).
const maxSeconds = math.MaxInt64 / int64(time.Second)

// maxDays is the largest day total (days plus weeks, as days) that
// parse accepts. The bound exists for overflow safety alone: Add
// converts the total to an int for AddDate, which is 32 bits wide on a
// 32-bit build.
//
// The bound is deliberately not a storage rule. Whether a duration
// carries a time past the storable range depends on the start it
// applies to, and parse has no start. The callers that add a span to a
// time own that check (see timeutil.Storable).
const maxDays = math.MaxInt32

type parsed struct {
	neg     bool
	weeks   int64
	days    int64
	hours   int64
	minutes int64
	seconds int64
}

// checkRange rejects a duration whose components are out of range. The
// day total must fit maxDays. The hours, minutes, and seconds total
// must fit maxSeconds. The per-component guards run before the sum, so
// the arithmetic itself cannot wrap. Each bounded component
// contributes at most maxSeconds. Three such terms stay far below
// math.MaxInt64.
func (p parsed) checkRange(orig string) error {
	if p.weeks > maxDays/7 || p.days > maxDays-p.weeks*7 {
		return fmt.Errorf("invalid duration %q: too large, the day total is more than %d days", orig, int64(maxDays))
	}
	if p.hours > maxSeconds/3600 || p.minutes > maxSeconds/60 || p.seconds > maxSeconds ||
		p.hours*3600+p.minutes*60+p.seconds > maxSeconds {
		return fmt.Errorf("invalid duration %q: too large, the time total is more than %d seconds", orig, maxSeconds)
	}
	return nil
}

// consumeComponent extracts the unsigned integer before letter in s.
// If letter is absent, returns (s, 0, nil). Per RFC 5545 a component
// value is one or more DIGITs with no embedded sign. The whole-duration
// sign is handled once in parse, so "PT-1H" and "PT+1H" are rejected.
func consumeComponent(s string, letter byte, orig string) (string, int64, error) {
	i := strings.IndexByte(s, letter)
	if i < 0 {
		return s, 0, nil
	}
	if i == 0 {
		return "", 0, fmt.Errorf("invalid duration %q: %c requires a number", orig, letter)
	}
	num := s[:i]
	// strconv.ParseInt rejects every non-digit except a leading sign. A
	// first-byte sign check therefore forbids the per-component signs.
	// num is non-empty here, because i > 0.
	// The 64-bit width keeps the range checks the same on a 32-bit
	// build. strconv.Atoi is int-sized there. A value at a ceiling
	// would then fail the parse instead of the range check.
	if num[0] == '+' || num[0] == '-' {
		return "", 0, fmt.Errorf("invalid duration %q: bad %c value %q", orig, letter, num)
	}
	v, err := strconv.ParseInt(num, 10, 64)
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
// Also returns an error when a component is out of range. checkRange
// caps the time total at maxSeconds (about 292 years). It caps the day
// total at maxDays. Both bounds exist for overflow safety.
func Validate(s string) error {
	_, err := parse(s)
	return err
}

// ValidateSpan checks that s is a well-formed, positive RFC 5545
// duration. A DURATION property that expresses a span (a VEVENT end,
// a VTODO duration) must be positive per RFC 5545 §3.8.2.5. Triggers
// are different: a VALARM TRIGGER may be negative, so trigger callers
// use Validate.
func ValidateSpan(s string) error {
	p, err := parse(s)
	if err != nil {
		return err
	}
	if p.neg || (p.weeks|p.days|p.hours|p.minutes|p.seconds) == 0 {
		return fmt.Errorf("invalid duration %q: a span must be positive", s)
	}
	return nil
}

// ValidateOptionalSpan is ValidateSpan for a field the caller can leave
// unset. An empty string means "no span" and passes. Every optional
// span column shares this rule: the event duration, the todo duration,
// and the two export guards that read them.
func ValidateOptionalSpan(s string) error {
	if s == "" {
		return nil
	}
	return ValidateSpan(s)
}

// UsableSpan reports whether s is a span the caller can emit: present
// and valid. The two exporters share this rule. It is the opposite test
// to ValidateOptionalSpan, which passes an absent value.
func UsableSpan(s string) bool {
	return s != "" && ValidateSpan(s) == nil
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

	// checkRange bounds the components, so the day total fits an int32
	// and the AddDate conversion is safe on a 32-bit build.
	days := int(p.days + p.weeks*7)
	d := time.Duration(p.hours)*time.Hour +
		time.Duration(p.minutes)*time.Minute +
		time.Duration(p.seconds)*time.Second

	if p.neg {
		return t.AddDate(0, 0, -days).Add(-d)
	}
	return t.AddDate(0, 0, days).Add(d)
}
