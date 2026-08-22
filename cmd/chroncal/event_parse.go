package main

// The flag-parse and validation helpers in this file are shared by the
// event, todo, and journal commands. The name reflects where the code
// came from, not its only caller.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
)

// resolveEventOccurrence looks up an event by ID, UID, or UID plus
// recurrence-id. When no override row exists, it returns the generated
// occurrence from the series master. createOverride is true in that case
// so event update can call UpdateInstance.
func resolveEventOccurrence(ctx context.Context, a *app.App, ref, recurrenceID string) (event.Event, bool, error) {
	if recurrenceID == "" {
		e, err := resolveEvent(ctx, a, ref, "")
		return e, false, err
	}
	if _, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		e, err := resolveEvent(ctx, a, ref, recurrenceID)
		return e, false, err
	}
	e, err := a.Events.GetByUIDAndRecurrenceID(ctx, ref, recurrenceID)
	if err == nil {
		return e, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return event.Event{}, false, err
	}
	master, err := a.Events.GetByUID(ctx, ref)
	if err != nil {
		return event.Event{}, false, notFoundErr(err, "event", ref)
	}
	at, err := parseRFC3339Flag("recurrence-id", recurrenceID)
	if err != nil {
		return event.Event{}, false, err
	}
	occ, ok := generatedEventOccurrence(master, at)
	if !ok {
		return event.Event{}, false, errInvalidInputf("--recurrence-id %s is not an occurrence of event %s", at.Format(time.RFC3339), ref)
	}
	return occ, true, nil
}

func generatedEventOccurrence(master event.Event, at time.Time) (event.Event, bool) {
	want := at.UTC().Format(time.RFC3339)
	from := at.UTC()
	to := from.Add(time.Second)
	for _, inst := range recurrence.ExpandEvent(master, from, to) {
		if inst.InstanceTime.UTC().Format(time.RFC3339) != want {
			continue
		}
		e := inst.Event
		if !e.EndTime.IsZero() {
			e.EndTime = inst.InstanceTime.Add(e.EndTime.Sub(e.StartTime))
		}
		e.StartTime = inst.InstanceTime
		e.RecurrenceID = want
		return e, true
	}
	return event.Event{}, false
}

func parseRFC3339Flag(flag, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, errInvalidInputf("--%s: invalid RFC 3339 timestamp %q", flag, value)
	}
	return t.UTC(), nil
}

// startTime is the event's start time. When a date-only value (YYYY-MM-DD) is
// provided for a timed event, the start time's hour and minute are overlaid onto
// the parsed date. EXDATE/RDATE values then match the recurrence instance time
// per RFC 5545 Section 3.8.5.1.
func parseDateFlags(flags []string, tz string, startTime time.Time) (string, error) {
	loc := time.Local
	if tz != "" {
		var err error
		loc, err = time.LoadLocation(tz)
		if err != nil {
			return "", fmt.Errorf("load timezone %q: %w", tz, err)
		}
	}
	var out []string
	for _, val := range flags {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		var t time.Time
		var err error
		dateOnly := false
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02T15:04",
			"2006-01-02",
		} {
			if layout == time.RFC3339 {
				t, err = time.Parse(layout, val)
			} else {
				t, err = time.ParseInLocation(layout, val, loc)
			}
			if err == nil {
				if layout == "2006-01-02" {
					dateOnly = true
				}
				break
			}
		}
		if err != nil {
			return "", errInvalidInputf("parse date %q: expected YYYY-MM-DD or YYYY-MM-DDTHH:MM", val)
		}
		// For date-only values on timed events, overlay the event's start
		// time so that the EXDATE/RDATE matches the recurrence instance.
		if dateOnly && !startTime.IsZero() {
			stIn := startTime.In(loc)
			t = time.Date(t.Year(), t.Month(), t.Day(), stIn.Hour(), stIn.Minute(), stIn.Second(), 0, loc)
		}
		// For date-only values with no start time (todos), preserve
		// the date-only format so export emits VALUE=DATE correctly.
		if dateOnly && startTime.IsZero() {
			out = append(out, t.Format("2006-01-02"))
		} else {
			out = append(out, t.UTC().Format(time.RFC3339))
		}
	}
	return strings.Join(out, ","), nil
}

// parseOrganizerFlag parses the --organizer flag into an Attendee with Organizer=true.
// Accepts email or "Name <email>".
func parseOrganizerFlag(val string) model.Attendee {
	var name, email string
	if idx := strings.Index(val, "<"); idx >= 0 {
		name = strings.TrimSpace(val[:idx])
		email = strings.TrimRight(val[idx+1:], ">")
	} else {
		email = val
	}
	return model.Attendee{
		Email:      email,
		Name:       name,
		Role:       "CHAIR",
		RSVPStatus: "ACCEPTED",
		Organizer:  true,
	}
}

// parseRelationFlags parses --related-to flag values into Relation models.
// Each value can be:
//   - A UID: "some-event-uid" (defaults to RELTYPE=PARENT)
//   - "RELTYPE:uid": "PARENT:uid", "CHILD:uid", "SIBLING:uid"
func parseRelationFlags(flags []string) ([]model.Relation, error) {
	validTypes := map[string]bool{"PARENT": true, "CHILD": true, "SIBLING": true}
	var out []model.Relation
	for _, val := range flags {
		relType := "PARENT"
		uid := val
		if idx := strings.Index(val, ":"); idx > 0 {
			prefix := strings.ToUpper(val[:idx])
			if validTypes[prefix] {
				relType = prefix
				uid = val[idx+1:]
			}
		}
		if uid == "" {
			return nil, errInvalidInputf("--related-to %q: UID must not be empty", val)
		}
		out = append(out, model.Relation{RelType: relType, RelUID: uid})
	}
	return out, nil
}

// parseAttendeeFlags parses --attendee flag values into Attendee models.
// Each value can be:
//   - An email address: "user@example.com"
//   - "Name <email>": "Alice <alice@example.com>"
func parseAttendeeFlags(flags []string) []model.Attendee {
	out := make([]model.Attendee, 0, len(flags))
	for _, val := range flags {
		var name, email string
		if idx := strings.Index(val, "<"); idx >= 0 {
			name = strings.TrimSpace(val[:idx])
			email = strings.TrimRight(val[idx+1:], ">")
		} else {
			email = val
		}
		out = append(out, model.Attendee{
			Email:      email,
			Name:       name,
			Role:       "REQ-PARTICIPANT",
			RSVPStatus: "NEEDS-ACTION",
		})
	}
	return out
}

// mergeAttendeeUpdate computes the attendee set to persist on a partial update.
//
// Organizer and attendees share one storage table, but ReplaceAttendees is a
// full replace. A slice from only the flags that were passed wipes the other
// kind of row (issue #461). So each --flag replaces only its own kind of row.
// When --attendee is absent, the stored non-organizer attendees are kept.
// When --organizer is absent, the stored organizer is kept.
// Both flags together are a full replace. --organizer "" clears it.
func mergeAttendeeUpdate(existing []model.Attendee, attendeeChanged bool, newAttendees []model.Attendee, organizerChanged bool, organizer string) []model.Attendee {
	out := make([]model.Attendee, 0, len(existing)+len(newAttendees)+1)
	if attendeeChanged {
		out = append(out, newAttendees...)
	} else {
		for _, a := range existing {
			if !a.Organizer {
				out = append(out, a)
			}
		}
	}
	if organizerChanged {
		if organizer != "" {
			out = append(out, parseOrganizerFlag(organizer))
		}
	} else {
		for _, a := range existing {
			if a.Organizer {
				out = append(out, a)
			}
		}
	}
	return out
}

// parseAlarmFlags parses --alarm flag values into Alarm models.
//
// Simple format (backward compatible):
//
//	"-PT15M"              → DISPLAY, 15min before start
//	"EMAIL:-PT1H"         → EMAIL, 1h before start
//
// Extended format (duration triggers only):
//
//	"ACTION:TRIGGER:DESC:REPEAT:DURATION:RELATED:ATTENDEES"
//	"DISPLAY:-PT30M::3:PT5M:END"                          → repeat 3x every 5min, relative to END
//	"EMAIL:-PT1H:::::alice@example.com,bob@example.com"    → EMAIL with attendees
//
// Extended format is only available for duration triggers (prefix -, +, or P).
// Absolute RFC 3339 triggers do not support additional fields.
func parseAlarmFlags(flags []string) ([]model.Alarm, error) {
	var out []model.Alarm
	warnedMissingSMTP := false
	for _, val := range flags {
		a, err := parseOneAlarm(val)
		if err != nil {
			return nil, err
		}
		if a.Action == "EMAIL" && !warnedMissingSMTP && !smtpConfiguredForEmailAlarms() {
			fmt.Fprintf(os.Stderr, "chroncal: warning: EMAIL alarm added without SMTP configuration (set CHRONCAL_SMTP_HOST or [smtp].host); alarm will behave as DISPLAY until SMTP is configured\n")
			warnedMissingSMTP = true
		}
		if a.Action == "EMAIL" && len(a.Attendees) == 0 {
			fmt.Fprintf(os.Stderr, "chroncal: warning: EMAIL alarm has no attendees (RFC 5545 requires at least one; alarm will behave as DISPLAY)\n")
		}
		out = append(out, a)
	}
	return out, nil
}

func smtpConfiguredForEmailAlarms() bool {
	return strings.TrimSpace(cfg.SMTP.Host) != ""
}

// The help renders once at package load, from the joined lists the
// model exports, so it stays in lockstep with the model sets.
var alarmFlagHelp = `alarm in format [ACTION:]TRIGGER[:DESC:REPEAT:DURATION:RELATED:ATTENDEES]; ACTION is one of ` +
	model.AlarmActionsList() + ` (default ` + model.DefaultAlarmAction + `); extended fields are optional, e.g. "DISPLAY:-PT30M::3:PT5M:END" or "EMAIL:-PT1H:::::user@example.com"; repeatable`

// clearForeignAlarmsHelp documents the escape from the carry-over rule.
// The --alarm flag cannot state an action this app does not fire, so an
// update keeps those rows by default.
const clearForeignAlarmsHelp = `delete the stored alarms this app cannot fire, such as a server "no reminder" sentinel or the alarm of another client; the default keeps them, because an --alarm edit cannot state them`

func parseOneAlarm(val string) (model.Alarm, error) {
	action := model.DefaultAlarmAction
	rest := val

	// Check for ACTION: prefix
	if idx := strings.Index(val, ":"); idx > 0 {
		prefix := strings.ToUpper(val[:idx])
		// Only a fireable action is a valid prefix. The CLI creates local
		// alarms, and a local alarm must be one the engine can fire. A
		// sync-only action (issue #579) enters the database from import
		// alone.
		if model.FireableAlarmAction(prefix) {
			action = prefix
			rest = val[idx+1:]
		}
	}

	if rest == "" {
		return model.Alarm{}, fmt.Errorf("alarm %q: missing trigger value", val)
	}

	// Determine if the trigger is a duration (can have extended fields) or
	// an absolute datetime (no extended fields, since RFC 3339 contains colons).
	isDuration := rest[0] == '-' || rest[0] == '+' || rest[0] == 'P'

	a := model.Alarm{
		Action:      action,
		Description: "Reminder",
		Related:     model.DefaultAlarmRelated,
	}

	if !isDuration {
		// Absolute trigger — no splitting on colons.
		if err := validateAlarmTrigger(rest); err != nil {
			return model.Alarm{}, fmt.Errorf("alarm %q: %w", val, err)
		}
		// Normalize RFC 3339 to iCal UTC format for consistent storage.
		if t, err := time.Parse(time.RFC3339, rest); err == nil {
			a.TriggerValue = t.UTC().Format("20060102T150405Z")
		} else {
			a.TriggerValue = rest // Already iCal format or other valid form.
		}
		return a, nil
	}

	// Duration trigger — split into positional fields.
	// Fields: trigger, description, repeat, duration, related, attendees
	parts := strings.SplitN(rest, ":", 6)
	a.TriggerValue = parts[0]

	if err := validateAlarmTrigger(a.TriggerValue); err != nil {
		return model.Alarm{}, fmt.Errorf("alarm %q: %w", val, err)
	}

	// Parse optional fields from the extended format.
	if len(parts) > 1 && parts[1] != "" {
		a.Description = parts[1]
	}
	if len(parts) > 2 && parts[2] != "" {
		r, err := strconv.Atoi(parts[2])
		if err != nil || r < 0 || r > model.MaxAlarmRepeat {
			return model.Alarm{}, fmt.Errorf("alarm %q: invalid repeat count %q (0-%d)", val, parts[2], model.MaxAlarmRepeat)
		}
		a.Repeat = r
	}
	if len(parts) > 3 && parts[3] != "" {
		if !model.ValidAlarmDuration(parts[3]) {
			return model.Alarm{}, fmt.Errorf("alarm %q: repeat duration %q must be a positive RFC 5545 duration within the supported range", val, parts[3])
		}
		a.Duration = parts[3]
	}
	if len(parts) > 4 && parts[4] != "" {
		rel := strings.ToUpper(parts[4])
		if !model.ValidAlarmRelated(rel) {
			return model.Alarm{}, fmt.Errorf("alarm %q: related must be one of %s, got %q", val, model.AlarmRelatedValuesList(), parts[4])
		}
		a.Related = rel
	}
	if len(parts) > 5 && parts[5] != "" {
		for _, email := range strings.Split(parts[5], ",") {
			email = strings.TrimSpace(email)
			if email == "" {
				continue
			}
			if !strings.Contains(email, "@") {
				return model.Alarm{}, fmt.Errorf("alarm %q: invalid attendee email %q", val, email)
			}
			a.Attendees = append(a.Attendees, model.AlarmAttendee{Email: email})
		}
	}

	// Cross-field validation per RFC 5545.
	if !a.RepeatPaired() {
		return model.Alarm{}, fmt.Errorf("alarm %q: REPEAT and DURATION must be specified together", val)
	}

	return a, nil
}

// parseAttachFlags parses --attach flag values into Attachment models.
// Each value can be:
//   - A file path (read as blob, MIME inferred from extension)
//   - "mime/type:path" (blob with explicit MIME)
//   - A URL that contains "://" (URI attachment)
//   - "mime/type:url" (URI with explicit MIME)
func parseAttachFlags(flags []string) ([]model.Attachment, error) {
	var out []model.Attachment
	for _, val := range flags {
		var fmttype, target string

		// Check for explicit MIME prefix like "application/pdf:/path/to/file"
		if idx := strings.Index(val, ":"); idx > 0 {
			prefix := val[:idx]
			if strings.Contains(prefix, "/") && !strings.Contains(prefix, "://") {
				fmttype = prefix
				target = val[idx+1:]
			} else {
				target = val
			}
		} else {
			target = val
		}

		if strings.Contains(target, "://") {
			// URI attachment
			out = append(out, model.Attachment{URI: target, FmtType: fmttype})
		} else {
			// File path — read as blob
			data, err := os.ReadFile(target)
			if err != nil {
				return nil, fmt.Errorf("read attachment %q: %w", target, err)
			}
			if fmttype == "" {
				fmttype = mime.TypeByExtension(filepath.Ext(target))
			}
			out = append(out, model.Attachment{
				Data:     data,
				Filename: filepath.Base(target),
				FmtType:  fmttype,
			})
		}
	}
	return out, nil
}

// validateEventEnums checks that status, class, transparency, and priority
// values are valid per RFC 5545. Empty strings are allowed (defaults apply).
func validateEventEnums(status, class, transp string, priority int64) error {
	if status != "" {
		switch strings.ToUpper(status) {
		case "TENTATIVE", "CONFIRMED", "CANCELLED":
		default:
			return errInvalidInputf("invalid --status %q: must be TENTATIVE, CONFIRMED, or CANCELLED", status)
		}
	}
	if class != "" {
		switch strings.ToUpper(class) {
		case "PUBLIC", "PRIVATE", "CONFIDENTIAL":
		default:
			return errInvalidInputf("invalid --class %q: must be PUBLIC, PRIVATE, or CONFIDENTIAL", class)
		}
	}
	if transp != "" {
		switch strings.ToUpper(transp) {
		case "OPAQUE", "TRANSPARENT":
		default:
			return errInvalidInputf("invalid --transparency %q: must be OPAQUE or TRANSPARENT", transp)
		}
	}
	if priority < 0 || priority > 9 {
		return errInvalidInputf("invalid --priority %d: must be 0-9", priority)
	}
	return nil
}

// validateRRule checks that an RRULE value contains a valid FREQ per RFC 5545
// Section 3.3.10. Empty string is allowed (optional field).
func validateRRule(rrule string) error {
	if rrule == "" {
		return nil
	}
	validFreqs := map[string]bool{
		"SECONDLY": true, "MINUTELY": true, "HOURLY": true,
		"DAILY": true, "WEEKLY": true, "MONTHLY": true, "YEARLY": true,
	}
	for _, part := range strings.Split(strings.ToUpper(rrule), ";") {
		if strings.HasPrefix(part, "FREQ=") {
			freq := strings.TrimPrefix(part, "FREQ=")
			if !validFreqs[freq] {
				return errInvalidInputf("invalid --rrule FREQ=%s: must be one of SECONDLY, MINUTELY, HOURLY, DAILY, WEEKLY, MONTHLY, YEARLY", freq)
			}
			return nil
		}
	}
	return errInvalidInputf("invalid --rrule %q: must contain FREQ= (e.g. FREQ=WEEKLY;BYDAY=MO)", rrule)
}

// validateGeo checks that a GEO value is "lat;lon" with valid ranges per
// RFC 5545 Section 3.8.1.6. Empty string is allowed (optional field).
func validateGeo(geo string) error {
	if geo == "" {
		return nil
	}
	parts := strings.SplitN(geo, ";", 2)
	if len(parts) != 2 {
		return errInvalidInputf("invalid --geo %q: must be lat;lon (e.g. 37.386;-122.083)", geo)
	}
	lat, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return errInvalidInputf("invalid --geo latitude %q: must be a number", parts[0])
	}
	lon, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return errInvalidInputf("invalid --geo longitude %q: must be a number", parts[1])
	}
	if lat < -90 || lat > 90 {
		return errInvalidInputf("invalid --geo latitude %.6f: must be between -90 and 90", lat)
	}
	if lon < -180 || lon > 180 {
		return errInvalidInputf("invalid --geo longitude %.6f: must be between -180 and 180", lon)
	}
	return nil
}

// validateURL checks that a URL has a scheme per RFC 3986. Empty string is
// allowed (optional field).
func validateURL(u string) error {
	if u == "" {
		return nil
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return errInvalidInputf("invalid --url %q: %v", u, err)
	}
	if parsed.Scheme == "" {
		return errInvalidInputf("invalid --url %q: must include a scheme (e.g. https://example.com)", u)
	}
	return nil
}

// validateAlarmTrigger checks that a trigger value is a valid ISO 8601
// duration (e.g. -PT15M, P1D) or an absolute RFC 3339 datetime per
// RFC 5545 Section 3.8.6.3.
func validateAlarmTrigger(trigger string) error {
	if trigger == "" {
		return errInvalidInputf("alarm trigger must not be empty")
	}
	// The CLI accepts a strict subset of model.ParseableAlarmTrigger:
	// durations, compact UTC, and RFC 3339. The CLI does not accept a
	// compact floating time (no trailing Z). The flag has no TZID
	// context, the value would store raw, and export would emit
	// invalid iCal (issue #572 documents that failure). The duration
	// error carries the reason. A well-formed duration can fail the
	// range check, and a generic message would misdescribe that
	// rejection.
	if model.AlarmTriggerIsDuration(trigger) {
		if err := duration.Validate(trigger); err != nil {
			return errInvalidInputf("invalid alarm trigger: %v (use an ISO 8601 duration such as -PT15M, or an RFC 3339 datetime)", err)
		}
		return nil
	}
	if _, err := model.ParseAbsoluteTimeUTC(trigger); err == nil {
		return nil
	}
	// ParseAbsoluteTime accepts one more layout than the strict parser
	// above: the compact floating form. A value that reaches here and
	// parses is therefore floating.
	if _, err := model.ParseAbsoluteTime(trigger, ""); err == nil {
		return errInvalidInputf("invalid alarm trigger %q: a floating time has no timezone; add a Z suffix or use an RFC 3339 datetime with an offset", trigger)
	}
	return errInvalidInputf("invalid alarm trigger %q (use an ISO 8601 duration such as -PT15M, or an RFC 3339 datetime)", trigger)
}
