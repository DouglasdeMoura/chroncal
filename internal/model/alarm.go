package model

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/duration"
)

// MaxAlarmRepeat caps RFC 5545 REPEAT counts. The spec sets no bound. Every
// repeat becomes a tracked trigger time in the alarm check loop. An
// absurd value (imported or typed) would hang or OOM every check.
const MaxAlarmRepeat = 100

type Alarm struct {
	ID            int64
	EventID       int64
	UID           string // globally unique per RFC 9074
	Action        string // DISPLAY, EMAIL, AUDIO
	TriggerValue  string // e.g. "-PT15M" or absolute RFC 3339
	Description   string
	Summary       string // RFC 5545 SUMMARY (required for EMAIL action)
	Repeat        int    // number of additional repetitions
	Duration      string // repeat interval (RFC 5545 duration, e.g. PT5M)
	Related       string // trigger anchor: START or END
	Acknowledged  string // RFC 9074 ACKNOWLEDGED UTC timestamp (round-trip only, does not affect local alarm_state)
	AttachURI     string // optional sound URI for AUDIO alarms (RFC 5545 Section 3.6.6)
	AttachBinary  []byte // optional inline (ENCODING=BASE64;VALUE=BINARY) sound for AUDIO alarms
	AttachFmtType string // FMTTYPE param for ATTACH (e.g. "audio/basic")
	Attendees     []AlarmAttendee
	XProperties   []XProperty // X-* extension props, round-trip only
}

// ContentEqual returns true if two alarms have identical content (all fields
// except ID, EventID, UID, Acknowledged, and XProperties). Used by
// ReplaceAlarms to match new alarms against stored ones for
// merge-based updates. XProperties are excluded so a remote X-prop tweak
// does not break the match and lose alarm state. Matched alarms get their
// X-properties rewritten unconditionally instead.
func (a Alarm) ContentEqual(b Alarm) bool {
	if !strings.EqualFold(a.Action, b.Action) {
		return false
	}
	if !triggerValuesEqual(a.TriggerValue, b.TriggerValue) {
		return false
	}
	// Related picks the anchor a duration offset applies to, so it changes
	// when a duration alarm fires — but it means nothing on an absolute
	// trigger: RFC 5545 forbids the param there, export omits it, and import
	// round-trips it as the default START. Comparing it for absolute triggers
	// would let a stored pre-normalization "END" break this match on the next
	// pull and re-fire an already-acknowledged reminder. The triggers already
	// compared equal above, so checking one side's kind covers both.
	if duration.Validate(a.TriggerValue) == nil && !strings.EqualFold(a.Related, b.Related) {
		return false
	}
	if a.Description != b.Description || a.Summary != b.Summary {
		return false
	}
	if a.Repeat != b.Repeat || a.Duration != b.Duration {
		return false
	}
	if a.AttachURI != b.AttachURI || a.AttachFmtType != b.AttachFmtType {
		return false
	}
	if !bytes.Equal(a.AttachBinary, b.AttachBinary) {
		return false
	}
	return attendeesEqual(a.Attendees, b.Attendees)
}

// triggerValuesEqual compares two alarm trigger values. It normalizes absolute
// time formats. iCal UTC (20060102T150405Z), RFC 3339, and iCal floating
// (20060102T150405) are all recognized. The same instant written in
// different formats is then treated as equal.
func triggerValuesEqual(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	ta, okA := parseTriggerTime(a)
	tb, okB := parseTriggerTime(b)
	return okA && okB && ta.Equal(tb)
}

func parseTriggerTime(s string) (time.Time, bool) {
	t, err := ParseAbsoluteTime(s, "")
	return t, err == nil
}

// ParseAbsoluteTime parses an absolute iCal datetime value in any of the three
// recognized forms: iCal UTC (20060102T150405Z), iCal floating (20060102T150405),
// or RFC 3339. A floating value is interpreted in timezone (a tz database name)
// when non-empty and loadable; otherwise it is returned with its zero offset.
func ParseAbsoluteTime(value, timezone string) (time.Time, error) {
	if t, err := time.Parse("20060102T150405Z", value); err == nil {
		return t, nil
	}
	if t, err := time.Parse("20060102T150405", value); err == nil {
		if timezone != "" {
			if loc, lerr := time.LoadLocation(timezone); lerr == nil {
				return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), nil
			}
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid trigger format: %q", value)
}

func attendeesEqual(a, b []AlarmAttendee) bool {
	if len(a) != len(b) {
		return false
	}
	ae := sortedEmails(a)
	be := sortedEmails(b)
	for i := range ae {
		if ae[i] != be[i] {
			return false
		}
	}
	return true
}

func sortedEmails(atts []AlarmAttendee) []string {
	emails := make([]string, len(atts))
	for i, a := range atts {
		emails[i] = strings.ToLower(a.Email)
	}
	sort.Strings(emails)
	return emails
}

// ValidAlarmAction returns true if action is a value the alarm tables
// accept. The set mirrors the CHECK constraints in db/migrations/003 and
// 006. Keep this function and the two constraints in lockstep. A value
// that passes here but fails the constraint rolls back the whole resource
// transaction during sync (issue #575).
func ValidAlarmAction(action string) bool {
	switch action {
	case "AUDIO", "DISPLAY", "EMAIL":
		return true
	}
	return false
}

// ValidAlarmRelated returns true if related is a value the alarm tables
// accept for the TRIGGER anchor. The set mirrors the same CHECK
// constraints as ValidAlarmAction, with the same lockstep rule.
func ValidAlarmRelated(related string) bool {
	return related == "START" || related == "END"
}

// ValidAlarmDuration returns true if d is a repeat interval the alarm
// engine can fire: a well-formed RFC 5545 duration that advances time.
// A negative interval walks the repeat triggers backwards. A zero
// interval never advances them. The import parser, the CLI parser, and
// the exporter share this rule.
func ValidAlarmDuration(d string) bool {
	if duration.Validate(d) != nil {
		return false
	}
	var t time.Time
	return duration.Add(t, d).After(t)
}

// RepeatPaired reports whether REPEAT and DURATION are both set or both
// absent. RFC 5545 §3.8.6.3 forbids one without the other.
func (a Alarm) RepeatPaired() bool {
	return (a.Repeat > 0) == (a.Duration != "")
}

// ValidateAcknowledged returns true if v is a valid RFC 9074 ACKNOWLEDGED
// value: empty string (clear), iCal UTC datetime, or RFC 3339.
func ValidateAcknowledged(v string) bool {
	if v == "" {
		return true
	}
	if _, err := time.Parse("20060102T150405Z", v); err == nil {
		return true
	}
	if _, err := time.Parse(time.RFC3339, v); err == nil {
		return true
	}
	return false
}

type AlarmAttendee struct {
	ID    int64
	Email string
	Name  string
}
