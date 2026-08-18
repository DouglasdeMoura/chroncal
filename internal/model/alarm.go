package model

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
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

// alarmActions lists the ACTION values the alarm tables accept, in a
// fixed order. The set mirrors the CHECK constraints in db/migrations/003
// and 006. Keep this slice and the two constraints in lockstep. A value
// outside this set fails the constraint and rolls back the whole resource
// transaction during sync (issue #575). The match is case-sensitive, the
// same as the constraints. The slice is unexported so no other package
// can change the accepted set at run time.
var alarmActions = []string{"AUDIO", "DISPLAY", "EMAIL"}

// alarmRelatedValues lists the RELATED values the alarm tables accept
// for the TRIGGER anchor, with the same lockstep rule as alarmActions.
var alarmRelatedValues = []string{"START", "END"}

// AlarmActions returns the accepted ACTION values as a fresh slice.
// A caller can keep or change the returned slice. The accepted set
// does not change.
func AlarmActions() []string {
	return slices.Clone(alarmActions)
}

// AlarmRelatedValues returns the accepted RELATED values as a fresh
// slice, like AlarmActions.
func AlarmRelatedValues() []string {
	return slices.Clone(alarmRelatedValues)
}

// AlarmActionsList returns the accepted ACTION values as one joined
// string, for error messages and help text. The list renders once at
// package load, so every consumer stays in lockstep with the set.
func AlarmActionsList() string {
	return alarmActionsList
}

// AlarmRelatedValuesList returns the accepted RELATED values as one
// joined string, like AlarmActionsList.
func AlarmRelatedValuesList() string {
	return alarmRelatedValuesList
}

// ValidAlarmAction returns true if action is an accepted ACTION value.
func ValidAlarmAction(action string) bool {
	return slices.Contains(alarmActions, action)
}

// ValidAlarmRelated returns true if related is an accepted RELATED
// value.
func ValidAlarmRelated(related string) bool {
	return slices.Contains(alarmRelatedValues, related)
}

// Default values for the alarm fields that PrepareAlarmsForWrite fills
// when the caller leaves them empty.
const (
	DefaultAlarmAction  = "DISPLAY"
	DefaultAlarmRelated = "START"
)

// The joined lists render once at package load, so every error message
// stays in lockstep with the slices.
var (
	alarmActionsList       = strings.Join(alarmActions, ", ")
	alarmRelatedValuesList = strings.Join(alarmRelatedValues, ", ")
)

// ErrInvalidAlarm marks an alarm field value the alarm tables reject.
// PrepareAlarmsForWrite wraps it with the field name and the value.
var ErrInvalidAlarm = errors.New("invalid alarm")

// PrepareAlarmsForWrite returns a prepared copy of alarms. For each
// element it fills an empty Action with DefaultAlarmAction and an
// empty Related with DefaultAlarmRelated. It then validates the two
// fields against the sets the alarm tables accept. Those are the two
// alarm columns with a CHECK constraint. For a bad value it returns an
// error that wraps ErrInvalidAlarm and names the index, the field, and
// the value. The caller's slice does not change.
//
// The event and todo services call this function before every alarm
// write, as defense in depth. The iCal import parser and the CLI
// parser stay the first guards. The sync engine retries a resource on
// any ReplaceAlarms error. A bad value that reaches sync still blocks
// that resource. The typed error names the cause instead of a raw
// CHECK failure (issues #575, #578).
func PrepareAlarmsForWrite(alarms []Alarm) ([]Alarm, error) {
	prepared := slices.Clone(alarms)
	for i := range prepared {
		if err := prepareAlarmForWrite(&prepared[i]); err != nil {
			return nil, fmt.Errorf("alarm %d: %w", i, err)
		}
	}
	return prepared, nil
}

// prepareAlarmForWrite fills the defaults and validates one alarm.
func prepareAlarmForWrite(a *Alarm) error {
	if a.Action == "" {
		a.Action = DefaultAlarmAction
	}
	if a.Related == "" {
		a.Related = DefaultAlarmRelated
	}
	if !ValidAlarmAction(a.Action) {
		return fmt.Errorf("%w: action %q is not one of %s", ErrInvalidAlarm, a.Action, alarmActionsList)
	}
	if !ValidAlarmRelated(a.Related) {
		return fmt.Errorf("%w: related %q is not one of %s", ErrInvalidAlarm, a.Related, alarmRelatedValuesList)
	}
	return nil
}

// ValidAlarmDuration returns true if d is a repeat interval the alarm
// engine can fire: a well-formed RFC 5545 duration that advances time.
// A negative interval walks the repeat triggers backwards. A zero
// interval never advances them. The import parser, the CLI parser, the
// exporter, and the repeat fire path share this rule.
//
// One duration.Add call answers both "well-formed" and "advances time".
// Add returns the zero time when d does not parse, and the base here is
// the zero time, so an unparseable, empty, zero, or negative d all land
// at or before the base. The predicate runs on the alarm check loop, so
// it must not parse d twice.
func ValidAlarmDuration(d string) bool {
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
