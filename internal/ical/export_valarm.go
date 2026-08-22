package ical

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// exportableTrigger reports whether a stored TRIGGER value can be emitted as
// RFC 5545-valid iCal. That is, whether it is a duration or a date-time.
//
// Import now rejects such a value outright (see parseAlarm), so this is a
// backstop rather than the primary defense. It catches rows written by the
// window in which import preserved the raw value. It also catches a value a
// future caller writes directly. Without it, buildValarm would label the
// value VALUE=DATE-TIME. Strict CalDAV servers would reject the malformed
// VALARM with HTTP 400. The PUT for the whole resource would then fail. The
// resource would stay permanently dirty.
//
// A skip of the VALARM is itself lossy. The PUT deletes that alarm from the
// server copy. That is why import drops the value up front, where the user
// gets a warning. Do not let it reach this point in silence.
//
// A floating date-time trigger is resolved against the record's timezone in
// buildValarm. The alarm engine reads the same value through
// model.ParseAbsoluteTime with the record's timezone, so export and fire time
// agree by construction (issue #572). Do not read a floating value as UTC
// without the record's timezone. That normalization was reverted once, and it
// moved reminders by the zone offset.
func exportableTrigger(v string) bool {
	return model.ParseableAlarmTrigger(v)
}

// buildValarm renders an alarm as a VALARM component, or nil when the alarm
// carries a TRIGGER that cannot be expressed as valid iCal. recordTZ is the
// owning event or todo timezone, and it resolves a floating absolute trigger
// the way the alarm engine does. Callers must skip a nil result.
func buildValarm(alarm model.Alarm, recordTZ string) *ical.Component {
	if !exportableTrigger(alarm.TriggerValue) {
		return nil
	}
	valarm := ical.NewComponent(ical.CompAlarm)
	if alarm.UID != "" {
		valarm.Props.SetText(ical.PropUID, alarm.UID)
	}
	// Normalize the action once. A value that is not an RFC 5545
	// iana-token or x-name cannot be written: a bare or malformed
	// "ACTION:" line is invalid iCal, and a strict server rejects the
	// whole resource for it. The write rule refuses such a value now
	// (issue #595), and the services normalize a stored row as they read
	// it (issue #607). This guard therefore covers an in-memory alarm
	// that reached the exporter without either path.
	//
	// The two cases take different fallbacks. An empty action is an unset
	// value, and DISPLAY is the default the parser and the write rule fill
	// in. A malformed non-empty action belongs to the VALARM of another
	// client, so it takes the reserved x-name instead. DISPLAY would push
	// that alarm to the server as a firing reminder (issue #603).
	action := alarm.Action
	switch {
	case action == "":
		action = model.DefaultAlarmAction
	case !model.ValidAlarmActionToken(action):
		action = model.UnsupportedAlarmAction
	}
	valarm.Props.SetText(ical.PropAction, action)

	// exportableTrigger above guarantees a non-empty value that is either a
	// duration or a date-time, so the VALUE parameter is never a guess.
	trigger := &ical.Prop{Name: ical.PropTrigger, Params: make(ical.Params)}
	trigger.Value = alarm.TriggerValue
	if alarm.TriggerValue[0] == '-' || alarm.TriggerValue[0] == '+' || alarm.TriggerValue[0] == 'P' {
		trigger.Params.Set("VALUE", "DURATION")
		if alarm.Related == "END" {
			trigger.Params.Set("RELATED", "END")
		}
	} else {
		// RFC 5545 §3.8.6.3: the trigabs production permits only
		// VALUE=DATE-TIME on an absolute trigger — never RELATED. parseAlarm
		// now drops RELATED on absolute triggers at import, but a stored
		// Related == "END" can still reach here from a pre-normalization DB
		// row or from the alarm editor (which preserves Related across edits),
		// and it is inert for an absolute trigger; emitting it gets the VALARM
		// rejected with HTTP 400 by strict CalDAV servers, failing the PUT for
		// the whole resource. So this guard stays even though import no longer
		// produces the case.
		trigger.Params.Set("VALUE", "DATE-TIME")
		// Resolve every absolute value through ParseAbsoluteTime, the same
		// function computeTriggerTimeForInstance uses. A floating value then
		// denotes the same instant here and at fire time (issue #572). The
		// call also normalizes legacy RFC 3339 rows to compact UTC.
		if t, err := model.ParseAbsoluteTime(alarm.TriggerValue, recordTZ); err == nil {
			trigger.Value = t.UTC().Format("20060102T150405Z")
		}
	}
	valarm.Props.Set(trigger)

	if alarm.Description != "" {
		valarm.Props.SetText(ical.PropDescription, alarm.Description)
	}
	if alarm.Summary != "" {
		valarm.Props.SetText(ical.PropSummary, alarm.Summary)
	}
	// RFC 5545 §3.8.6.3: DURATION and REPEAT MUST appear together; emitting
	// either one without the other yields an invalid VALARM that strict CalDAV
	// servers (e.g. Google) reject with HTTP 400, blocking the whole resource.
	// The ValidAlarmDuration guard exists for DB rows written before the
	// parsers validated DURATION (the same reason as the RFC 3339 branch
	// above). A stored bad value must not reach the server verbatim.
	if alarm.Repeat > 0 && model.ValidAlarmDuration(alarm.Duration) {
		p := &ical.Prop{Name: ical.PropDuration}
		p.Value = alarm.Duration
		valarm.Props.Set(p)
		p2 := &ical.Prop{Name: "REPEAT"}
		// Clamp like import does. A pre-clamp DB row must not push a
		// count the next pull would rewrite. A preserved sync-only alarm
		// keeps its count, because it must round-trip verbatim and it
		// never expands into trigger state (issue #579).
		repeat := alarm.Repeat
		if model.FireableAlarmAction(action) {
			repeat = min(repeat, model.MaxAlarmRepeat)
		}
		p2.Value = strconv.Itoa(repeat)
		valarm.Props.Set(p2)
	}
	// ACKNOWLEDGED (RFC 9074) — round-trip only.
	if alarm.Acknowledged != "" {
		p := &ical.Prop{Name: "ACKNOWLEDGED", Params: make(ical.Params)}
		p.Value = alarm.Acknowledged
		// Normalize RFC 3339 to iCal UTC format.
		if t, err := time.Parse(time.RFC3339, alarm.Acknowledged); err == nil {
			p.Value = t.UTC().Format("20060102T150405Z")
		}
		valarm.Props.Set(p)
	}

	for _, att := range alarm.Attendees {
		p := &ical.Prop{Name: ical.PropAttendee, Params: make(ical.Params)}
		p.Value = "mailto:" + att.Email
		if att.Name != "" {
			p.Params.Set(ical.ParamCommonName, att.Name)
		}
		valarm.Props.Add(p)
	}

	// ATTACH: a sound for AUDIO alarms or a document for EMAIL alarms
	// (RFC 5545 §3.6.6). Emitted as an inline BASE64 blob or a URI.
	// DISPLAY alarms carry no ATTACH, so drop it for that one action.
	// The fold covers a legacy lowercase "display" row: the wide CHECK
	// stores it, and it is semantically a DISPLAY alarm.
	//
	// A preserved sync-only action (issue #579) keeps its ATTACH. The
	// RFC leaves the property set of an x-name or iana-token action
	// open. The VALARM of another client must round-trip verbatim.
	if !strings.EqualFold(action, "DISPLAY") && (len(alarm.AttachBinary) > 0 || alarm.AttachURI != "") {
		p := &ical.Prop{Name: ical.PropAttach, Params: make(ical.Params)}
		if len(alarm.AttachBinary) > 0 {
			p.Value = base64.StdEncoding.EncodeToString(alarm.AttachBinary)
			p.Params.Set("ENCODING", "BASE64")
			p.Params.Set("VALUE", "BINARY")
		} else {
			p.Value = alarm.AttachURI
		}
		if alarm.AttachFmtType != "" {
			p.Params.Set("FMTTYPE", alarm.AttachFmtType)
		}
		valarm.Props.Add(p)
	}

	emitXProperties(valarm, alarm.XProperties)

	return valarm
}
