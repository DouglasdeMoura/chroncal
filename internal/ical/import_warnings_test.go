package ical

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// An unparseable DUE or DTSTART on a VTODO is dropped. It cannot be stored.
// A drop of it in silence is data loss the user never sees. The todo leaves
// every due-date view. The next export re-emits the VTODO without it.
func TestImport_UnparseableTodoDates_Warn(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:bad-due@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DUE:2026-04-01\r\n" +
		"DTSTART:nonsense\r\n" +
		"SUMMARY:Todo with bad dates\r\n" +
		"END:VTODO\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Todos) != 1 {
		t.Fatalf("todos = %d, want 1", len(result.Todos))
	}
	var sawDue, sawStart bool
	for _, w := range result.Warnings {
		if strings.Contains(w, "DUE") {
			sawDue = true
		}
		if strings.Contains(w, "DTSTART") {
			sawStart = true
		}
	}
	if !sawDue {
		t.Errorf("no warning for the unparseable DUE; warnings = %v", result.Warnings)
	}
	if !sawStart {
		t.Errorf("no warning for the unparseable DTSTART; warnings = %v", result.Warnings)
	}
}

// A dropped-alarm warning must name the record that lost the alarm. The
// unparseable TRIGGER is dropped rather than stored. The warning is then the
// only trace. In a large import, "alarm dropped" with no owner is unactionable.
// Follows the parseDateProp precedent: `<kind> "<uid>": ...`.
func TestImport_DroppedAlarmWarning_NamesOwningRecord(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:alarm-owner-event@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with bad trigger\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:P\r\n" +
		"DESCRIPTION:Broken\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:alarm-owner-todo@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:Todo with bad trigger\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"TRIGGER:soon\r\n" +
		"DESCRIPTION:Broken\r\n" +
		"END:VALARM\r\n" +
		"END:VTODO\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if !warningMentions(result.Warnings, `event "alarm-owner-event@example.com"`, "TRIGGER", `"P"`) {
		t.Errorf("event alarm warning does not name its owner; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, `todo "alarm-owner-todo@example.com"`, "TRIGGER", `"soon"`) {
		t.Errorf("todo alarm warning does not name its owner; warnings = %v", result.Warnings)
	}
}

// eventICS builds a VCALENDAR with one VEVENT and the given VALARM bodies.
// Each valarm string holds the property lines of one VALARM, with CRLF
// line ends.
func eventICS(uid, summary string, valarms ...string) string {
	s := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:" + summary + "\r\n"
	for _, v := range valarms {
		s += "BEGIN:VALARM\r\n" + v + "END:VALARM\r\n"
	}
	return s + "END:VEVENT\r\nEND:VCALENDAR\r\n"
}

// Regression test for issue #579 (follow-up to issue #575). PR #577
// dropped a VALARM with an action outside the fireable set. Every later
// push then deleted the VALARM of another client from the server copy.
// For ACTION:NONE, the drop let Google re-apply the reminders the user
// turned off. The parser must now keep such an alarm verbatim, and the
// warning must tell the user the alarm never fires locally.
func TestImport_UnsupportedAlarmAction_PreservesAlarm(t *testing.T) {
	t.Parallel()
	ics := eventICS("action-none-event@example.com", "Event with a sync-only alarm",
		"ACTION:NONE\r\nTRIGGER:-PT15M\r\n",
		"ACTION:X-APPLE-SOUND\r\nTRIGGER:-PT5M\r\n",
		"ACTION:DISPLAY\r\nTRIGGER:-PT30M\r\nDESCRIPTION:Real reminder\r\n",
	)

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 3 {
		t.Fatalf("events/alarms = %d/%+v, want 1 event with 3 alarms", len(result.Events), result.Events)
	}
	wantActions := []string{"NONE", "X-APPLE-SOUND", "DISPLAY"}
	for i, want := range wantActions {
		if got := result.Events[0].Alarms[i].Action; got != want {
			t.Errorf("alarm %d action = %q, want %q (preserved verbatim)", i, got, want)
		}
	}
	if !warningMentions(result.Warnings, `event "action-none-event@example.com"`, "ACTION", `"NONE"`, "never fires locally") {
		t.Errorf("no preserved-action warning for NONE; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, `"X-APPLE-SOUND"`, "never fires locally") {
		t.Errorf("no preserved-action warning for X-APPLE-SOUND; warnings = %v", result.Warnings)
	}
}

// The caller's TriggerValue gate drops a sync-only alarm with an
// unparseable TRIGGER. The report must then carry only the dropped warning.
// A "preserved" warning next to a "dropped" warning for one alarm would
// contradict itself.
func TestImport_SyncOnlyAlarmWithBadTrigger_OneCoherentWarning(t *testing.T) {
	t.Parallel()
	ics := eventICS("sync-only-bad-trigger@example.com", "Event with a broken foreign alarm",
		"ACTION:X-APPLE-SOUND\r\nTRIGGER:soon\r\n",
	)

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 0 {
		t.Fatalf("events/alarms = %d/%+v, want 1 event with 0 alarms", len(result.Events), result.Events)
	}
	if !warningMentions(result.Warnings, "TRIGGER", `"soon"`, "alarm dropped") {
		t.Errorf("no dropped-trigger warning; warnings = %v", result.Warnings)
	}
	if warningMentions(result.Warnings, "preserved for sync") {
		t.Errorf("contradictory preserved warning for a dropped alarm; warnings = %v", result.Warnings)
	}
}

// An unsupported RELATED value and a malformed DURATION share the failure
// class of issue #575. RELATED fails the CHECK constraint and rolls back
// the resource. DURATION round-trips into a push that a strict server
// rejects. The parser must warn about both values and must not store them.
func TestImport_UnsupportedAlarmRelatedAndDuration_DegradeWithWarning(t *testing.T) {
	t.Parallel()
	ics := eventICS("bad-related-event@example.com", "Event with a bad RELATED and DURATION",
		"ACTION:DISPLAY\r\nTRIGGER;RELATED=STARTS:-PT15M\r\nREPEAT:2\r\nDURATION:5 minutes\r\n",
	)

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 1 {
		t.Fatalf("events/alarms = %d/%v, want 1 event with 1 alarm", len(result.Events), result.Events)
	}
	alarm := result.Events[0].Alarms[0]
	if alarm.Related != "START" {
		t.Errorf("Related = %q, want the START default", alarm.Related)
	}
	if alarm.Duration != "" {
		t.Errorf("Duration = %q, want empty", alarm.Duration)
	}
	if alarm.Repeat != 0 {
		t.Errorf("Repeat = %d, want 0 (RFC 5545 pairs REPEAT with DURATION)", alarm.Repeat)
	}
	if !warningMentions(result.Warnings, `event "bad-related-event@example.com"`, "RELATED", "STARTS") {
		t.Errorf("no RELATED warning that names the event; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, `event "bad-related-event@example.com"`, "DURATION", `"5 minutes"`) {
		t.Errorf("no DURATION warning that names the event; warnings = %v", result.Warnings)
	}
}

// Recoverable VALARM defects must not lose the reminder. The parser keeps
// the DISPLAY default for an empty ACTION. It clears a REPEAT that has no
// DURATION (RFC 5545 pairs them). It drops a negative DURATION together
// with its REPEAT, in one warning. It drops the DURATION of an explicit
// REPEAT:0 with an accurate message. It clamps an absurd REPEAT and warns.
func TestImport_RecoverableAlarmDefects_KeepAlarm(t *testing.T) {
	t.Parallel()
	ics := eventICS("recoverable-alarm-event@example.com", "Event with recoverable alarm defects",
		"ACTION:\r\nTRIGGER:-PT15M\r\nREPEAT:3\r\n",
		"ACTION:DISPLAY\r\nTRIGGER:-PT30M\r\nREPEAT:2\r\nDURATION:-PT5M\r\n",
		"ACTION:DISPLAY\r\nTRIGGER:-PT45M\r\nREPEAT:0\r\nDURATION:PT5M\r\n",
		"ACTION:DISPLAY\r\nTRIGGER:-PT1H\r\nREPEAT:5000\r\nDURATION:PT5M\r\n",
	)

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 4 {
		t.Fatalf("events/alarms = %d/%+v, want 1 event with 4 alarms", len(result.Events), result.Events)
	}
	alarms := result.Events[0].Alarms
	for i, alarm := range alarms {
		if alarm.Action != "DISPLAY" {
			t.Errorf("alarm %d: Action = %q, want DISPLAY", i, alarm.Action)
		}
	}
	for i, alarm := range alarms[:3] {
		if alarm.Repeat != 0 || alarm.Duration != "" {
			t.Errorf("alarm %d: Repeat/Duration = %d/%q, want the cleared pair", i, alarm.Repeat, alarm.Duration)
		}
	}
	if alarms[3].Repeat != model.MaxAlarmRepeat || alarms[3].Duration != "PT5M" {
		t.Errorf("alarm 3: Repeat/Duration = %d/%q, want the clamp %d with the kept interval",
			alarms[3].Repeat, alarms[3].Duration, model.MaxAlarmRepeat)
	}
	if !warningMentions(result.Warnings, "REPEAT and DURATION", "REPEAT dropped") {
		t.Errorf("no unpaired REPEAT warning; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, `"-PT5M"`, "dropped with its REPEAT") {
		t.Errorf("no combined negative-DURATION warning; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, "REPEAT:0", "DURATION dropped") {
		t.Errorf("no REPEAT:0 warning; warnings = %v", result.Warnings)
	}
	if !warningMentions(result.Warnings, "clamped") {
		t.Errorf("no clamp warning; warnings = %v", result.Warnings)
	}
}

// parseAlarm reports every problem it finds. A single warning slot meant the
// ATTACH message overwrote the "will not fire" one. The user then saw a broken
// sound file and never learned the alarm was dead.
func TestImport_AlarmWithTwoProblems_ReportsBoth(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:two-alarm-problems@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T100000Z\r\n" +
		"DTEND:20260401T110000Z\r\n" +
		"SUMMARY:Doubly broken alarm\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:AUDIO\r\n" +
		"TRIGGER:not-a-time\r\n" +
		"ATTACH;ENCODING=BASE64;VALUE=BINARY:!!!not-base64!!!\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	joined := strings.Join(result.Warnings, " | ")
	if !strings.Contains(joined, "TRIGGER") {
		t.Errorf("TRIGGER warning lost; warnings = %v", result.Warnings)
	}
	if !strings.Contains(joined, "ATTACH") {
		t.Errorf("ATTACH warning lost; warnings = %v", result.Warnings)
	}
}

// The parser preserves only an ACTION that is an RFC 5545 iana-token or
// x-name. A malformed value cannot round-trip: export would emit an
// invalid ACTION line, and a strict server rejects the whole resource.
// The parser drops that alarm and warns. A well-formed x-name survives.
func TestImport_MalformedActionToken_DropsAlarm(t *testing.T) {
	t.Parallel()
	ics := eventICS("bad-action-token@example.com", "Event with a malformed action",
		"ACTION:NO NE\r\nTRIGGER:-PT15M\r\n",
		"ACTION:X-APPLE-SOUND\r\nTRIGGER:-PT5M\r\n",
	)

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Alarms) != 1 {
		t.Fatalf("events/alarms = %d/%+v, want 1 event with 1 alarm", len(result.Events), result.Events)
	}
	if got := result.Events[0].Alarms[0].Action; got != "X-APPLE-SOUND" {
		t.Errorf("surviving alarm action = %q, want X-APPLE-SOUND", got)
	}
	if !warningMentions(result.Warnings, `"NO NE"`, "dropped") {
		t.Errorf("no malformed-ACTION warning; warnings = %v", result.Warnings)
	}
}

// RFC 5545 permits an x-name or an iana-token for PARTSTAT, ROLE, and
// CUTYPE, but each attendee column carries a CHECK constraint. Before
// the clamp, a server-sent x-name value failed the insert inside the
// sync transaction and rolled back the whole resource on every pass
// (issue #587). The parser now clamps the value and warns.
func TestImport_UnsupportedAttendeeParams_ClampWithWarning(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:x-name-partstat@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event with foreign attendee params\r\n" +
		"ATTENDEE;PARTSTAT=X-FOO;ROLE=X-BAR;CUTYPE=X-CUSTOM:mailto:a@example.com\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Attendees) != 1 {
		t.Fatalf("events/attendees = %d/%+v, want 1 event with 1 attendee", len(result.Events), result.Events)
	}
	a := result.Events[0].Attendees[0]
	if a.RSVPStatus != model.DefaultRSVPStatus {
		t.Errorf("RSVPStatus = %q, want the %s default", a.RSVPStatus, model.DefaultRSVPStatus)
	}
	if a.Role != model.DefaultAttendeeRole {
		t.Errorf("Role = %q, want the %s default", a.Role, model.DefaultAttendeeRole)
	}
	if a.CUType != model.UnknownCUType {
		t.Errorf("CUType = %q, want %s", a.CUType, model.UnknownCUType)
	}

	// The clamped values must pass the write rule, so the resource
	// persists instead of failing the pull.
	if _, err := model.PrepareAttendeesForWrite(model.EventAttendee, result.Events[0].Attendees); err != nil {
		t.Fatalf("the clamped attendee must pass the write rule: %v", err)
	}

	for _, want := range []string{"PARTSTAT", "ROLE", "CUTYPE"} {
		if !warningMentions(result.Warnings, `event "x-name-partstat@example.com"`, want) {
			t.Errorf("no %s clamp warning that names the event; warnings = %v", want, result.Warnings)
		}
	}
}

// A VTODO accepts COMPLETED and IN-PROCESS on PARTSTAT, because its
// table carries the wider CHECK constraint. The clamp must not degrade
// those two values to the default.
func TestImport_TaskPartStat_KeepsCompleted(t *testing.T) {
	t.Parallel()
	const ics = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//test//EN\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:task-partstat@example.com\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:Todo with a task PARTSTAT\r\n" +
		"ATTENDEE;PARTSTAT=COMPLETED:mailto:a@example.com\r\n" +
		"END:VTODO\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Todos) != 1 || len(result.Todos[0].Attendees) != 1 {
		t.Fatalf("todos/attendees = %d/%+v, want 1 todo with 1 attendee", len(result.Todos), result.Todos)
	}
	if got := result.Todos[0].Attendees[0].RSVPStatus; got != "COMPLETED" {
		t.Errorf("RSVPStatus = %q, want COMPLETED", got)
	}
	for _, w := range result.Warnings {
		if strings.Contains(w, "PARTSTAT") {
			t.Errorf("a valid task PARTSTAT must not warn; got %q", w)
		}
	}
}
