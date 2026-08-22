package ical

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// parseAlarm extracts a model.Alarm from a VALARM component.
// The second return value is a warning string (empty if no issues). A VALARM
// with several problems reports all of them. Each one changes what the alarm
// does in silence. The user needs to see every reason.
func parseAlarm(comp *ical.Component) (model.Alarm, string) {
	alarm := model.Alarm{Action: model.DefaultAlarmAction, Related: model.DefaultAlarmRelated}
	var warns []string

	if prop := comp.Props.Get(ical.PropAction); prop != nil {
		// The parser preserves an action outside model.FireableAlarmAction
		// (issue #579; that predicate's doc comment holds the shared
		// rationale). A drop makes every later push delete the VALARM of
		// the other client. The parser stores the trimmed original case,
		// so the foreign VALARM round-trips verbatim. A fireable action
		// stays canonical uppercase.
		raw := strings.TrimSpace(prop.Value)
		switch up := strings.ToUpper(raw); {
		case up == "":
			// An empty value keeps the default action. The service
			// write boundary (model.PrepareAlarmsForWrite) fills the
			// same default, and a reminder must not vanish over an
			// empty value.
		case model.FireableAlarmAction(up):
			alarm.Action = up
		case model.ValidAlarmActionToken(raw):
			alarm.Action = raw
		default:
			// A malformed action cannot round-trip: export would emit
			// an invalid ACTION line, and a strict server rejects the
			// whole resource with 400. Drop the alarm and warn.
			warns = append(warns, fmt.Sprintf("VALARM ACTION %q: not an RFC 5545 token; alarm dropped", raw))
			return model.Alarm{}, strings.Join(warns, "; ")
		}
	}
	if prop := comp.Props.Get(ical.PropTrigger); prop != nil {
		tv := prop.Value
		tzid := prop.Params.Get(ical.ParamTimezoneID)
		// Validate the trigger: one arm per value SHAPE, each format parsed
		// exactly once, with TZID consulted only where it matters (the
		// compact-floating form). Keep the accepted set in lockstep with
		// model.ParseAbsoluteTime — that function defines what a stored
		// absolute trigger MEANS, so a form accepted here but not there
		// would store alarms the fire path cannot read.
		isDuration := duration.Validate(tv) == nil
		valid := isDuration
		if !valid {
			if _, err := time.Parse("20060102T150405Z", tv); err == nil {
				// Already compact UTC — the canonical stored form.
				valid = true
			} else if t, err := time.Parse(time.RFC3339, tv); err == nil {
				// RFC 3339 carries its own offset (a TZID param, if present,
				// is redundant): normalize to compact UTC so a stored
				// absolute trigger has one encoding. Safe because the offset
				// is explicit — this is NOT the floating-value normalization
				// that was reverted (issue #572).
				tv = t.UTC().Format("20060102T150405Z")
				valid = true
			} else if t, err := time.Parse("20060102T150405", tv); err == nil {
				// Compact floating: resolve through the TZID when it loads;
				// otherwise keep the raw floating value, to be resolved
				// against the record's timezone at fire time.
				if tzid != "" {
					if loc, lerr := time.LoadLocation(tzid); lerr == nil {
						t = time.Date(t.Year(), t.Month(), t.Day(),
							t.Hour(), t.Minute(), t.Second(), 0, loc)
						tv = t.UTC().Format("20060102T150405Z")
					} else {
						warns = append(warns, fmt.Sprintf("VALARM TRIGGER TZID=%s: unknown timezone, treating as floating", tzid))
					}
				}
				valid = true
			}
		}
		if valid {
			alarm.TriggerValue = tv
		} else {
			// Unparseable (or empty) TRIGGER: leave TriggerValue empty so
			// the caller's `TriggerValue != ""` gate drops the alarm, and
			// warn.
			//
			// Preserving the raw value here looks like it protects round-trip
			// fidelity, but it cannot. The value is not expressible as valid
			// iCal, so the next push either emits a VALARM strict servers
			// reject with 400 — wedging the whole resource — or omits it and
			// deletes the alarm from the server anyway. Preserving only moves
			// that loss from "announced at import" to "silent at the next
			// push", and in the meantime stores an alarm every trigger-time
			// helper refuses while the CLI and TUI display it as an armed
			// reminder the alarm editor will not even open. Losing it loudly
			// is the honest trade.
			warns = append(warns, fmt.Sprintf("VALARM TRIGGER: unparseable value %q; alarm dropped (it could never fire)", tv))
		}
		// RELATED only means something on a duration trigger (it picks the
		// anchor the offset applies to). RFC 5545 §3.8.6.3's trigabs
		// production forbids RELATED on an absolute trigger, so export can
		// never emit it — a stored one would be junk that never round-trips
		// (push+pull resets it to START) and that silently resurfaces if the
		// user later switches the trigger to a duration. Keep the default
		// "START" for absolute triggers.
		if rel := strings.TrimSpace(prop.Params.Get("RELATED")); rel != "" && isDuration {
			// Same failure class as an unsupported ACTION (issue #575,
			// see model.ValidAlarmRelated).
			if up := strings.ToUpper(rel); model.ValidAlarmRelated(up) {
				alarm.Related = up
			} else {
				warns = append(warns, fmt.Sprintf("VALARM TRIGGER RELATED=%q: unsupported value, using %s", rel, model.DefaultAlarmRelated))
			}
		}
	} else {
		// RFC 5545 requires TRIGGER. Without it the caller's gate drops
		// the alarm, and the drop must not be silent.
		warns = append(warns, "VALARM TRIGGER: missing; alarm dropped (it could never fire)")
	}
	if prop := comp.Props.Get(ical.PropDescription); prop != nil {
		alarm.Description = prop.Value
	}
	if prop := comp.Props.Get(ical.PropSummary); prop != nil {
		alarm.Summary = prop.Value
	}
	repeatZero := false
	clampedFrom := 0
	if prop := comp.Props.Get("REPEAT"); prop != nil {
		// Clamp: REPEAT expands into per-trigger state in the check loop,
		// so an absurd imported value must not hang or OOM every check.
		// The clamp warning waits until after the pairing rule below.
		// Without the wait, one report could claim the clamp survived
		// and the repeat was dropped at the same time.
		v, err := strconv.Atoi(strings.TrimSpace(prop.Value))
		switch {
		case err != nil || v < 0:
			warns = append(warns, fmt.Sprintf("VALARM REPEAT: invalid value %q; ignored", prop.Value))
		case v > model.MaxAlarmRepeat && !model.FireableAlarmAction(alarm.Action):
			// A preserved sync-only alarm never expands into trigger
			// state, so the clamp guards nothing. A clamp here would
			// rewrite the VALARM of another client on the next push.
			alarm.Repeat = v
		case v > model.MaxAlarmRepeat:
			alarm.Repeat = model.MaxAlarmRepeat
			clampedFrom = v
		case v > 0:
			alarm.Repeat = v
		default:
			repeatZero = true // a valid "no repeats", not a defect
		}
	}
	if prop := comp.Props.Get(ical.PropDuration); prop != nil {
		// A malformed DURATION pushes verbatim, and a strict CalDAV
		// server rejects the whole resource with 400. See
		// model.ValidAlarmDuration for the value rule.
		if v := strings.TrimSpace(prop.Value); model.ValidAlarmDuration(v) {
			alarm.Duration = v
		} else if alarm.Repeat > 0 {
			// One defect, one warning: the REPEAT is unusable without
			// its interval. Name both drops here, so the pairing check
			// below finds a complete pair and adds no second warning.
			alarm.Repeat = 0
			warns = append(warns, fmt.Sprintf("VALARM DURATION: invalid value %q; dropped with its REPEAT", prop.Value))
		} else {
			warns = append(warns, fmt.Sprintf("VALARM DURATION: invalid value %q; dropped", prop.Value))
		}
	}
	// RFC 5545 §3.8.6.3 requires REPEAT and DURATION together. An unpaired
	// value cannot round-trip: buildValarm omits it, and the next pull then
	// deletes and recreates the alarm row, which loses the alarm state.
	// Clear the incomplete pair and name the dropped side. An explicit
	// REPEAT:0 with a DURATION is legal iCal. The row cannot store
	// "repeats disabled" apart from "REPEAT absent", so that pair cannot
	// round-trip either. It gets its own accurate message.
	switch {
	case repeatZero && alarm.Duration != "":
		alarm.Duration = ""
		warns = append(warns, "VALARM REPEAT:0: repeats disabled; DURATION dropped")
	case !alarm.RepeatPaired():
		dropped := "REPEAT"
		if alarm.Duration != "" {
			dropped = "DURATION"
		}
		alarm.Repeat = 0
		alarm.Duration = ""
		warns = append(warns, fmt.Sprintf("VALARM: REPEAT and DURATION must appear together; %s dropped", dropped))
	}
	if clampedFrom > 0 && alarm.Repeat > 0 {
		warns = append(warns, fmt.Sprintf("VALARM REPEAT: value %d clamped to %d", clampedFrom, model.MaxAlarmRepeat))
	}

	// UID (RFC 9074)
	if prop := comp.Props.Get(ical.PropUID); prop != nil {
		uid := strings.TrimSpace(prop.Value)
		if len(uid) > 0 && len(uid) <= 255 && !strings.ContainsRune(uid, 0) {
			alarm.UID = uid
		}
	}

	// ACKNOWLEDGED (RFC 9074) — preserved for round-trip fidelity only.
	if prop := comp.Props.Get("ACKNOWLEDGED"); prop != nil {
		v := strings.TrimSpace(prop.Value)
		if model.ValidateAcknowledged(v) && v != "" {
			alarm.Acknowledged = v
		}
	}

	// ATTACH (sound for AUDIO alarms): either a URI or an inline BASE64 blob.
	if prop := comp.Props.Get(ical.PropAttach); prop != nil {
		if prop.Params.Get("ENCODING") == "BASE64" {
			if data, err := decodeInlineAttachment(prop.Value); err != nil {
				warns = append(warns, fmt.Sprintf("VALARM ATTACH: %v", err))
			} else {
				alarm.AttachBinary = data
				alarm.AttachFmtType = prop.Params.Get("FMTTYPE")
			}
		} else {
			alarm.AttachURI = prop.Value
			alarm.AttachFmtType = prop.Params.Get("FMTTYPE")
		}
	}

	// ATTENDEE children (for EMAIL alarms)
	for _, prop := range comp.Props.Values(ical.PropAttendee) {
		alarm.Attendees = append(alarm.Attendees, model.AlarmAttendee{
			Email: stripMailto(prop.Value),
			Name:  prop.Params.Get(ical.ParamCommonName),
		})
	}

	// Every property parseAlarm does not read — preserved for round-trip
	// fidelity only. The set covers an X- extension and an IANA property
	// alike, for example the RFC 9074 PROXIMITY. A drop would rewrite the
	// VALARM of another client without it on the next push (issue #586).
	// Normalize to a non-nil slice: for ReplaceAlarms, non-nil means "this
	// is the complete X-prop set" (empty = remote cleared them), while nil
	// means "caller has no X-prop knowledge, keep stored rows".
	alarm.XProperties = extractXPropertiesWithSet(comp.Props, handledAlarmProps)
	if alarm.XProperties == nil {
		alarm.XProperties = []model.XProperty{}
	}

	// A preserved sync-only action needs a diagnostic: a legacy
	// ACTION:PROCEDURE or a typo would otherwise look like an armed
	// reminder. Warn only when the alarm survives the TRIGGER gate. The
	// report must not say "preserved" and "dropped" for one alarm. Sync
	// re-imports a resource only when it changes, so the warning does
	// not repeat on every pull.
	if alarm.TriggerValue != "" && !model.FireableAlarmAction(alarm.Action) {
		warns = append(warns, fmt.Sprintf("VALARM ACTION %q: preserved for sync; it never fires locally", alarm.Action))
	}

	return alarm, strings.Join(warns, "; ")
}

// handledAlarmProps is the set of property names parseAlarm reads into
// the alarm model. parseAlarm preserves every other property, so a
// foreign VALARM round-trips whole (issue #586).
var handledAlarmProps = map[string]bool{
	ical.PropAction: true, ical.PropTrigger: true, ical.PropDescription: true,
	ical.PropSummary: true, "REPEAT": true, ical.PropDuration: true,
	ical.PropUID: true, "ACKNOWLEDGED": true, ical.PropAttach: true,
	ical.PropAttendee: true,
}
