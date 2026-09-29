package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// weekdayNames maps each accepted weekday spelling to its weekday. The
// lookup is case-insensitive. A weekday word resolves to the next
// occurrence of that weekday. The current day counts as an occurrence, so
// "thursday" on a Thursday means today (issue #785).
var weekdayNames = map[string]time.Weekday{
	"monday":    time.Monday,
	"mon":       time.Monday,
	"tuesday":   time.Tuesday,
	"tue":       time.Tuesday,
	"tues":      time.Tuesday,
	"wednesday": time.Wednesday,
	"wed":       time.Wednesday,
	"thursday":  time.Thursday,
	"thu":       time.Thursday,
	"thur":      time.Thursday,
	"thurs":     time.Thursday,
	"friday":    time.Friday,
	"fri":       time.Friday,
	"saturday":  time.Saturday,
	"sat":       time.Saturday,
	"sunday":    time.Sunday,
	"sun":       time.Sunday,
}

// offsetPattern matches a signed day, week, or month count, for example
// "+3d", "-2w", or "+1m".
var offsetPattern = regexp.MustCompile(`^[+-]\d+[dwm]$`)

// maxOffsetCount bounds the offset count. A value beyond it cannot name a
// useful date. The resolver then reports the input as invalid instead of
// computing a date thousands of years away.
const maxOffsetCount = 1_000_000

// resolveRelativeDate resolves a relative date word against now in loc
// (issue #785). The vocabulary matches other terminal calendars:
//
//   - "today" and "now" resolve to the current calendar date.
//   - "tomorrow" and "yesterday" offset the current date by one day.
//   - A weekday name resolves to its next occurrence, today included.
//   - "next <weekday>" resolves to the first occurrence after today, as in
//     GNU date. This form disambiguates "thursday" on a Thursday.
//   - "+Nd", "+Nw", "+Nm" and their negative forms offset the current date.
//     A month offset uses calendar months, so January 31 plus one month
//     lands on March 3 after normalization.
//
// The result is the start of the resolved day in loc (see startOfDay). ok
// is false when value is not a relative form, or when the resolved year is
// outside 0000-9999. The caller then applies its strict format instead.
func resolveRelativeDate(value string, now time.Time, loc *time.Location) (time.Time, bool) {
	d, ok := relativeCivilDate(value, now, loc)
	if !ok {
		return time.Time{}, false
	}
	return startOfDay(d, loc), true
}

// relativeCivilDate resolves value like resolveRelativeDate, but returns the
// calendar date as midnight UTC. The arithmetic runs in UTC. In a zone that
// skips midnight for a daylight saving change, midnight in that zone moves
// to 23:00 of the day before. Arithmetic on that value then gives a date
// one day too early.
//
// ok is false when the resolved year is outside 0000-9999. The strict
// YYYY-MM-DD format cannot hold such a year, so a stored value would not
// parse again.
func relativeCivilDate(value string, now time.Time, loc *time.Location) (time.Time, bool) {
	d, ok := resolveCivilWord(value, now, loc)
	if !ok || d.Year() < 0 || d.Year() > 9999 {
		return time.Time{}, false
	}
	return d, true
}

// resolveCivilWord holds the vocabulary of relativeCivilDate. It does not
// check the year range.
func resolveCivilWord(value string, now time.Time, loc *time.Location) (time.Time, bool) {
	word := strings.ToLower(strings.TrimSpace(value))
	if word == "" {
		return time.Time{}, false
	}
	year, month, day := now.In(loc).Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)

	switch word {
	case "today", "now":
		return today, true
	case "tomorrow":
		return today.AddDate(0, 0, 1), true
	case "yesterday":
		return today.AddDate(0, 0, -1), true
	}

	if name, found := strings.CutPrefix(word, "next "); found {
		if weekday, known := weekdayNames[strings.TrimSpace(name)]; known {
			days := daysUntilWeekday(today, weekday)
			if days == 0 {
				days = 7
			}
			return today.AddDate(0, 0, days), true
		}
		return time.Time{}, false
	}

	if weekday, known := weekdayNames[word]; known {
		return today.AddDate(0, 0, daysUntilWeekday(today, weekday)), true
	}

	if offsetPattern.MatchString(word) {
		count, err := strconv.Atoi(word[1 : len(word)-1])
		if err != nil || count > maxOffsetCount {
			// The count exceeds the int range or the offset bound. Treat
			// the value as input to reject through the strict date format.
			return time.Time{}, false
		}
		if word[0] == '-' {
			count = -count
		}
		switch word[len(word)-1] {
		case 'd':
			return today.AddDate(0, 0, count), true
		case 'w':
			return today.AddDate(0, 0, 7*count), true
		case 'm':
			return today.AddDate(0, count, 0), true
		}
	}
	return time.Time{}, false
}

// startOfDay returns the first instant of the calendar date of civil in
// loc. Only the year, month, and day of civil apply. Usually the result is
// midnight in loc. In a zone that skips midnight for a daylight saving
// change, time.Date moves the missing midnight to the day before. The day
// then starts at the zone transition, so the result is that transition.
func startOfDay(civil time.Time, loc *time.Location) time.Time {
	year, month, day := civil.Date()
	t := time.Date(year, month, day, 0, 0, 0, 0, loc)
	if y, m, d := t.Date(); y != year || m != month || d != day {
		_, end := t.ZoneBounds()
		if !end.IsZero() {
			t = end
		}
	}
	return t
}

// dayAfter returns the start of the calendar day after the calendar date
// of t, in the location of t. Use it for an inclusive end date in a
// half-open range. t.AddDate(0, 0, 1) keeps the clock time of t, so it is
// not the start of the next day when t is a zone transition.
func dayAfter(t time.Time) time.Time {
	year, month, day := t.Date()
	return startOfDay(time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC), t.Location())
}

// parseCivilDate parses a strict YYYY-MM-DD value as a calendar date at
// midnight UTC. No local zone applies, so a skipped local midnight cannot
// change the date.
func parseCivilDate(value string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", value)
	return t, err == nil
}

// daysUntilWeekday returns the day count from today to the next occurrence
// of weekday. Zero means that today is the requested weekday.
func daysUntilWeekday(today time.Time, weekday time.Weekday) int {
	return (int(weekday) - int(today.Weekday()) + 7) % 7
}

// parseCLIDate parses a YYYY-MM-DD flag value, or a relative date word
// resolved against now in loc (issue #785). Pass one now per command, so a
// --from/--to pair cannot resolve across a midnight rollover. It replaces
// time.Parse's verbose "cannot parse / out of range" surface with a clean
// "--<flag>: invalid date ..." message.
func parseCLIDate(flag, value string, now time.Time, loc *time.Location) (time.Time, error) {
	return parseCLIDateAnchored(flag, value, now, loc, loc)
}

// parseCLIDateAnchored parses a date flag like parseCLIDate, but resolves a
// relative date word against the calendar day of now in anchor. The result
// is the start of the day in loc (see startOfDay). Use it when the user did not choose loc, for example
// when loc is the stored timezone of an event. "today" then means the day
// of the user, not the day in the zone of the event.
func parseCLIDateAnchored(flag, value string, now time.Time, anchor, loc *time.Location) (time.Time, error) {
	if d, ok := relativeCivilDate(value, now, anchor); ok {
		return startOfDay(d, loc), nil
	}
	d, ok := parseCivilDate(value)
	if !ok {
		return time.Time{}, errInvalidDate(flag, value)
	}
	return startOfDay(d, loc), nil
}

// errInvalidDate reports a date flag value that is neither YYYY-MM-DD nor
// a relative date word.
func errInvalidDate(flag, value string) error {
	return errInvalidInputf("--%s: invalid date %q (expected YYYY-MM-DD or a relative date such as today, tomorrow, friday, +7d)", flag, value)
}

// parseCLIDateString parses a date flag that the command stores as a plain
// YYYY-MM-DD string, for example a todo due date. Relative date words
// resolve against now in loc. The result is the canonical YYYY-MM-DD form
// of the calendar date. An empty value returns an empty string.
//
// The function keeps the calendar date and does not convert it to a
// midnight in loc. In a zone that skips midnight, that midnight falls on
// the day before, and the stored date would move back one day.
func parseCLIDateString(flag, value string, now time.Time, loc *time.Location) (string, error) {
	if value == "" {
		return "", nil
	}
	if d, ok := relativeCivilDate(value, now, loc); ok {
		return d.Format("2006-01-02"), nil
	}
	if _, ok := parseCivilDate(value); !ok {
		return "", errInvalidDate(flag, value)
	}
	return value, nil
}
