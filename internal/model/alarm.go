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
	// Exact case: the parser canonicalizes fireable actions to uppercase,
	// so a case fold gains nothing there. A preserved foreign action
	// keeps its original case (issue #579), and a case-only remote change
	// must land locally — a fold would skip the update and the next push
	// would write the stale case back over the other client's VALARM.
	if a.Action != b.Action {
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

// The iCal datetime layouts. The compact floating layout carries no
// zone, so only the callers that can resolve one accept it.
const (
	icalUTCLayout      = "20060102T150405Z"
	icalFloatingLayout = "20060102T150405"
)

// AlarmTriggerIsDuration reports whether v has the shape of a duration
// trigger. RFC 5545 durations start with P, and a trigger may carry a
// sign. The exporter, the CLI, and ParseableAlarmTrigger share the
// test, so the accepted shapes cannot drift.
func AlarmTriggerIsDuration(v string) bool {
	return v != "" && (v[0] == '-' || v[0] == '+' || v[0] == 'P')
}

// ParseAbsoluteTimeUTC parses an absolute iCal datetime that carries its
// own zone: iCal UTC (20060102T150405Z) or RFC 3339. It refuses the
// compact floating form. Use it where the caller has no timezone to
// resolve a floating value with, such as the CLI trigger flag.
func ParseAbsoluteTimeUTC(value string) (time.Time, error) {
	if t, err := time.Parse(icalUTCLayout, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid trigger format: %q", value)
}

// ParseAbsoluteTime parses an absolute iCal datetime value in any of the three
// recognized forms: iCal UTC (20060102T150405Z), iCal floating (20060102T150405),
// or RFC 3339. A floating value is interpreted in timezone (a tz database name)
// when non-empty and loadable; otherwise it is returned with its zero offset.
// The three layouts are mutually exclusive, so the zone-bearing forms can
// resolve first through ParseAbsoluteTimeUTC.
func ParseAbsoluteTime(value, timezone string) (time.Time, error) {
	if t, err := ParseAbsoluteTimeUTC(value); err == nil {
		return t, nil
	}
	if t, err := time.Parse(icalFloatingLayout, value); err == nil {
		if timezone != "" {
			if loc, lerr := time.LoadLocation(timezone); lerr == nil {
				return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), nil
			}
		}
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

// alarmActions lists the ACTION values the alarm engine can fire, in a
// fixed order. The TUI dropdown and the CLI --alarm parser offer only
// this set for a new alarm. The SQL queries that filter on the action
// copy this list, so the storage test
// TestFireableAlarmQueriesMatchModelPredicate probes every value here
// against those queries. The match is case-sensitive. The slice is
// unexported so no other package can change the set at run time.
//
// The storage rule is wider: see StorableAlarmAction. Migration 044
// widened the CHECK constraints, so the tables also hold a preserved
// foreign action (issue #579).
var alarmActions = []string{"AUDIO", "DISPLAY", "EMAIL"}

// alarmRelatedValues lists the RELATED values the alarm tables accept
// for the TRIGGER anchor, with the same lockstep rule as alarmActions.
var alarmRelatedValues = []string{"START", "END"}

// AlarmActions returns the fireable ACTION values as a fresh slice.
// A caller can keep or change the returned slice. The set does not
// change.
func AlarmActions() []string {
	return slices.Clone(alarmActions)
}

// AlarmRelatedValues returns the accepted RELATED values as a fresh
// slice, like AlarmActions.
func AlarmRelatedValues() []string {
	return slices.Clone(alarmRelatedValues)
}

// AlarmActionsList returns the fireable ACTION values as one joined
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

// FireableAlarmAction returns true if action is a value the alarm engine
// can fire. The alarm check loop skips every other action. The TUI
// dropdown and the CLI --alarm parser offer only this set for a new
// alarm.
//
// The storage rule is wider: see StorableAlarmAction. Import preserves an
// RFC 5545 x-name or iana-token action (for example X-APPLE-SOUND) and
// the Google ACTION:NONE sentinel (issue #579). A push then does not
// delete the alarm of another client.
func FireableAlarmAction(action string) bool {
	return slices.Contains(alarmActions, action)
}

// StorableAlarmAction returns true if action is a value the alarm tables
// accept: any non-empty string. The rule mirrors the CHECK constraints in
// db/migrations/044. Keep this function and the two constraints in
// lockstep. A value that passes here but fails the constraint rolls back
// the whole resource transaction during sync (issue #575).
func StorableAlarmAction(action string) bool {
	return action != ""
}

// CheckStorableAlarmAction returns a named error for an action the alarm
// tables reject. It converts an opaque CHECK failure into a named error.
// The enclosing transaction still rolls back on it. The alarm write
// helpers in the event and todo services share this check.
func CheckStorableAlarmAction(action string) error {
	if !StorableAlarmAction(action) {
		return fmt.Errorf("action %q is not storable", action)
	}
	return nil
}

// KeepSyncOnlyAlarms appends every stored alarm the engine cannot fire to
// a replacement list. A caller that can only state a fireable alarm — the
// --alarm flag has no syntax for a preserved action — must carry those
// rows forward. Without the carry-over the replacement deletes them, and
// the next push deletes the VALARM of another client (issue #579).
//
// A caller that must remove a preserved alarm skips this function and
// calls ReplaceAlarms with the exact list instead. The TUI alarm editor
// works that way, so a local calendar keeps a way to delete such a row.
func KeepSyncOnlyAlarms(stored, replacement []Alarm) []Alarm {
	for _, a := range stored {
		if !FireableAlarmAction(a.Action) {
			replacement = append(replacement, a)
		}
	}
	return replacement
}

// PrepareAlarmUpdate checks an alarm that a caller writes over the stored
// row ex. It returns the ACKNOWLEDGED value for the update. A malformed
// value that arrives must not clobber valid stored state, so the function
// keeps the stored value instead. The event service and the todo service
// share this rule.
func PrepareAlarmUpdate(a, ex Alarm) (string, error) {
	if err := CheckStorableAlarmAction(a.Action); err != nil {
		return "", fmt.Errorf("update alarm: %w", err)
	}
	if ValidateAcknowledged(a.Acknowledged) {
		return a.Acknowledged, nil
	}
	return ex.Acknowledged, nil
}

// ValidAlarmRelated returns true if related is a value the alarm tables
// accept for the TRIGGER anchor. The set mirrors the related CHECK
// constraints in db/migrations/003 and 006, with the same lockstep rule
// as StorableAlarmAction.
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
// fields against the rules the alarm tables enforce. Those are the two
// alarm columns with a CHECK constraint. For a bad value it returns an
// error that wraps ErrInvalidAlarm and names the index, the field, and
// the value. The caller's slice does not change.
//
// The event and todo services call this function before every alarm
// write. The iCal import parser and the CLI
// parser stay the first guards. A bad value that reaches sync still
// fails the import and retries. The typed error names the cause
// instead of a raw CHECK failure (issues #575, #578).
func PrepareAlarmsForWrite(alarms []Alarm) ([]Alarm, error) {
	prepared := slices.Clone(alarms)
	for i := range prepared {
		if err := prepareAlarmForWrite(&prepared[i]); err != nil {
			return nil, fmt.Errorf("alarm %d: %w", i+1, err)
		}
	}
	return prepared, nil
}

// prepareAlarmForWrite fills the defaults and validates one alarm.
//
// The action rule is StorableAlarmAction, not the fireable set. Migration
// 044 widened the CHECK constraint, because import preserves the action
// of another client (issue #579). A check against the fireable set here
// would reject every preserved alarm at the write boundary.
func prepareAlarmForWrite(a *Alarm) error {
	if a.Action == "" {
		a.Action = DefaultAlarmAction
	}
	if a.Related == "" {
		a.Related = DefaultAlarmRelated
	}
	if !StorableAlarmAction(a.Action) {
		return fmt.Errorf("%w: action %q is empty", ErrInvalidAlarm, a.Action)
	}
	if !ValidAlarmRelated(a.Related) {
		return fmt.Errorf("%w: related %q is not one of %s", ErrInvalidAlarm, a.Related, alarmRelatedValuesList)
	}
	return nil
}

// ValidAlarmDuration returns true if d is a repeat interval the alarm
// engine can fire: a well-formed, positive RFC 5545 duration. A
// negative interval walks the repeat triggers backwards. A zero
// interval never advances them. The import parser, the CLI parser, the
// exporter, and the repeat fire path share this rule.
//
// The predicate delegates to duration.ValidateSpan, the one positivity
// rule over the parser, with a single parse per call.
func ValidAlarmDuration(d string) bool {
	return duration.ValidateSpan(d) == nil
}

// RepeatPaired reports whether REPEAT and DURATION are both set or both
// absent. RFC 5545 §3.8.6.3 forbids one without the other.
func (a Alarm) RepeatPaired() bool {
	return (a.Repeat > 0) == (a.Duration != "")
}

// ParseableAlarmTrigger returns true if v is a trigger value the alarm
// engine and the exporter can read. It accepts a valid RFC 5545
// duration, or an absolute time that ParseAbsoluteTime accepts. A row
// that fails the test can never fire, and export omits its VALARM.
// The absolute branch delegates to ParseAbsoluteTime, so the accepted
// layouts have one owner.
func ParseableAlarmTrigger(v string) bool {
	if v == "" {
		return false
	}
	if AlarmTriggerIsDuration(v) {
		return duration.Validate(v) == nil
	}
	_, err := ParseAbsoluteTime(v, "")
	return err == nil
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
