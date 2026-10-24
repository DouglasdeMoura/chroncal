package icaltransfer_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// calendarICS wraps components in a VCALENDAR.
func calendarICS(components ...string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		strings.Join(components, "") + "END:VCALENDAR\r\n")
}

// valarmICS returns a VALARM. An empty uid gives no UID property.
func valarmICS(uid, action, trigger, description string) string {
	s := "BEGIN:VALARM\r\n"
	if uid != "" {
		s += "UID:" + uid + "\r\n"
	}
	s += "ACTION:" + action + "\r\nTRIGGER:" + trigger + "\r\n"
	if description != "" {
		s += "DESCRIPTION:" + description + "\r\n"
	}
	return s + "END:VALARM\r\n"
}

// veventICS returns a VEVENT with the alarms.
func veventICS(uid, summary string, alarms ...string) string {
	return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20261001T090000Z\r\n" +
		"DTSTART:20261101T090000Z\r\nDTEND:20261101T100000Z\r\nSUMMARY:" + summary + "\r\n" +
		strings.Join(alarms, "") + "END:VEVENT\r\n"
}

// vtodoICS returns a VTODO with the alarms.
func vtodoICS(uid, summary string, alarms ...string) string {
	return "BEGIN:VTODO\r\nUID:" + uid + "\r\nDTSTAMP:20261001T090000Z\r\n" +
		"DUE:20261101T090000Z\r\nSUMMARY:" + summary + "\r\n" +
		strings.Join(alarms, "") + "END:VTODO\r\n"
}

// twoCalendars creates the calendar of the alarm owner and the calendar of
// the round trip.
func twoCalendars(t *testing.T, a *app.App) (owner, roundTrip int64) {
	t.Helper()
	ctx := context.Background()
	o, err := a.Calendars.Create(ctx, "Imported", "", "")
	if err != nil {
		t.Fatalf("create owner calendar: %v", err)
	}
	r, err := a.Calendars.Create(ctx, "RoundTrip", "", "")
	if err != nil {
		t.Fatalf("create round-trip calendar: %v", err)
	}
	return o.ID, r.ID
}

// eventState returns the SEQUENCE and the alarm UIDs of the event uid.
func eventState(t *testing.T, a *app.App, uid string) (int64, []string) {
	t.Helper()
	ctx := context.Background()
	e, err := a.Events.GetByUID(ctx, uid)
	if err != nil {
		t.Fatalf("GetByUID %s: %v", uid, err)
	}
	alarms, err := a.Events.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatalf("ListAlarms %s: %v", uid, err)
	}
	return e.Sequence, alarmUIDs(alarms)
}

// todoState returns the SEQUENCE and the alarm UIDs of the todo uid.
func todoState(t *testing.T, a *app.App, uid string) (int64, []string) {
	t.Helper()
	ctx := context.Background()
	td, err := a.Todos.GetByUID(ctx, uid)
	if err != nil {
		t.Fatalf("GetByUID %s: %v", uid, err)
	}
	alarms, err := a.Todos.ListAlarms(ctx, td.ID)
	if err != nil {
		t.Fatalf("ListAlarms %s: %v", uid, err)
	}
	return td.Sequence, alarmUIDs(alarms)
}

func alarmUIDs(alarms []model.Alarm) []string {
	out := make([]string, len(alarms))
	for i, al := range alarms {
		out[i] = al.UID
	}
	return out
}

// requireUnchanged imports file twice into calendarID. Each run must count
// one unchanged item and keep the SEQUENCE and the alarm UIDs.
func requireUnchanged(t *testing.T, a *app.App, calendarID int64, file []byte, state func() (int64, []string), counts func(icaltransfer.Summary) (updated, unchanged int)) {
	t.Helper()
	wantSeq, wantUIDs := state()
	for run := range 2 {
		updated, unchanged := counts(importFixture(t, a, calendarID, file))
		if updated != 0 || unchanged != 1 {
			t.Errorf("run %d: updated = %d, unchanged = %d, want 0 and 1", run, updated, unchanged)
		}
		seq, uids := state()
		if seq != wantSeq {
			t.Errorf("run %d: sequence = %d, want %d", run, seq, wantSeq)
		}
		if strings.Join(uids, ",") != strings.Join(wantUIDs, ",") {
			t.Errorf("run %d: alarm UIDs = %v, want %v", run, uids, wantUIDs)
		}
	}
}

func eventCounts(s icaltransfer.Summary) (int, int) { return s.UpdatedEvents, s.UnchangedEvents }

func todoCounts(s icaltransfer.Summary) (int, int) { return s.UpdatedTodos, s.UnchangedTodos }

// TestImport_UnchangedItemWithAForeignAlarmUIDKeepsItsSequence covers the
// remaining case of issue #883 (issues #915, #922). The file holds a VALARM
// whose UID belongs to an alarm of another calendar. An alarm UID is unique
// in its item only, so the store keeps the UID of the file. The next
// imports of the same file must count as unchanged and keep the SEQUENCE.
func TestImport_UnchangedItemWithAForeignAlarmUIDKeepsItsSequence(t *testing.T) {
	const shared = "shared-alarm@test"
	for _, action := range []string{"DISPLAY", "NONE"} {
		t.Run("event "+action, func(t *testing.T) {
			a := newTestApp(t)
			owner, roundTrip := twoCalendars(t, a)
			importFixture(t, a, owner, calendarICS(veventICS("owner@test", "Owner",
				valarmICS(shared, action, "-PT15M", "Owner alarm"))))
			file := calendarICS(veventICS("round@test", "Round trip",
				valarmICS(shared, action, "-PT15M", "Round trip alarm")))
			importFixture(t, a, roundTrip, file)
			state := func() (int64, []string) { return eventState(t, a, "round@test") }
			requireAlarmUIDs(t, state, shared)
			requireUnchanged(t, a, roundTrip, file, state, eventCounts)
		})
		t.Run("todo "+action, func(t *testing.T) {
			a := newTestApp(t)
			owner, roundTrip := twoCalendars(t, a)
			importFixture(t, a, owner, calendarICS(vtodoICS("owner@test", "Owner",
				valarmICS(shared, action, "-PT15M", "Owner alarm"))))
			file := calendarICS(vtodoICS("round@test", "Round trip",
				valarmICS(shared, action, "-PT15M", "Round trip alarm")))
			importFixture(t, a, roundTrip, file)
			state := func() (int64, []string) { return todoState(t, a, "round@test") }
			requireAlarmUIDs(t, state, shared)
			requireUnchanged(t, a, roundTrip, file, state, todoCounts)
		})
	}
}

// requireAlarmUIDs checks the alarm UIDs that state returns.
func requireAlarmUIDs(t *testing.T, state func() (int64, []string), want ...string) {
	t.Helper()
	if _, got := state(); !slices.Equal(got, want) {
		t.Fatalf("alarm UIDs = %q, want %q", got, want)
	}
}

// TestImport_UnchangedItemWithADuplicateAlarmUIDKeepsItsSequence covers one
// event with two VALARMs of one UID. The second alarm cannot keep the UID
// either, so the store mints a UID for it.
func TestImport_UnchangedItemWithADuplicateAlarmUIDKeepsItsSequence(t *testing.T) {
	a := newTestApp(t)
	_, roundTrip := twoCalendars(t, a)
	alarm := valarmICS("dup-alarm@test", "DISPLAY", "-PT15M", "Reminder")
	file := calendarICS(veventICS("round@test", "Round trip", alarm, alarm))
	importFixture(t, a, roundTrip, file)
	state := func() (int64, []string) { return eventState(t, a, "round@test") }
	if _, uids := state(); len(uids) != 2 || uids[0] == uids[1] {
		t.Fatalf("alarm UIDs = %v, want two different UIDs", uids)
	}
	requireUnchanged(t, a, roundTrip, file, state, eventCounts)
}

// TestImport_MintedAlarmUIDWithNoTwinUpdatesOnce covers a row from before
// migration 063 that migration 064 cannot repair: no copy of the item holds
// the UID of the file. The next import writes the UID of the file once. The
// imports after that count as unchanged (issue #922).
func TestImport_MintedAlarmUIDWithNoTwinUpdatesOnce(t *testing.T) {
	const fileUID = "shared-alarm@test"
	ctx := context.Background()
	a := newTestApp(t)
	_, roundTrip := twoCalendars(t, a)
	file := calendarICS(veventICS("round@test", "Round trip",
		valarmICS(fileUID, "DISPLAY", "-PT15M", "Reminder")))
	importFixture(t, a, roundTrip, file)
	minted := "minted-before-063"
	if err := a.Queries.UpdateAlarmUID(ctx, storage.UpdateAlarmUIDParams{Uid: &minted, ID: alarmRowIDs(t, a, "round@test")[0]}); err != nil {
		t.Fatalf("UpdateAlarmUID: %v", err)
	}

	if s := importFixture(t, a, roundTrip, file); s.UpdatedEvents != 1 {
		t.Fatalf("updated events = %d, want 1", s.UpdatedEvents)
	}
	state := func() (int64, []string) { return eventState(t, a, "round@test") }
	requireAlarmUIDs(t, state, fileUID)
	requireUnchanged(t, a, roundTrip, file, state, eventCounts)
}

// TestImport_UnchangedItemIgnoresTheOrderOfAlarms covers a file that lists
// the alarms in another order than the row IDs of the store.
func TestImport_UnchangedItemIgnoresTheOrderOfAlarms(t *testing.T) {
	a := newTestApp(t)
	_, roundTrip := twoCalendars(t, a)
	first := valarmICS("first@test", "DISPLAY", "-PT15M", "First")
	second := valarmICS("second@test", "DISPLAY", "-PT5M", "Second")
	importFixture(t, a, roundTrip, calendarICS(veventICS("round@test", "Round trip", first, second)))
	state := func() (int64, []string) { return eventState(t, a, "round@test") }
	requireUnchanged(t, a, roundTrip, calendarICS(veventICS("round@test", "Round trip", second, first)), state, eventCounts)
}

// TestImport_ChangedAlarmUIDStillUpdates keeps the rule of issue #883. A
// file UID that the store can keep counts as content when it changes.
func TestImport_ChangedAlarmUIDStillUpdates(t *testing.T) {
	a := newTestApp(t)
	_, roundTrip := twoCalendars(t, a)
	importFixture(t, a, roundTrip, calendarICS(veventICS("round@test", "Round trip",
		valarmICS("old@test", "DISPLAY", "-PT15M", "Reminder"))))
	s := importFixture(t, a, roundTrip, calendarICS(veventICS("round@test", "Round trip",
		valarmICS("new@test", "DISPLAY", "-PT15M", "Reminder"))))
	if s.UpdatedEvents != 1 {
		t.Fatalf("updated events = %d, want 1", s.UpdatedEvents)
	}
}

// alarmRowIDs returns the alarm row IDs of the event uid.
func alarmRowIDs(t *testing.T, a *app.App, uid string) []int64 {
	t.Helper()
	ctx := context.Background()
	e, err := a.Events.GetByUID(ctx, uid)
	if err != nil {
		t.Fatalf("GetByUID %s: %v", uid, err)
	}
	alarms, err := a.Events.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatalf("ListAlarms %s: %v", uid, err)
	}
	out := make([]int64, len(alarms))
	for i, al := range alarms {
		out[i] = al.ID
	}
	return out
}

// TestImport_EditKeepsTheAlarmRows covers the alarm write of issues #915
// and #922. A changed item with a foreign or a duplicate alarm UID must keep
// its alarm rows, so the alarm state stays. The edit must not fail on the
// unique index either.
func TestImport_EditKeepsTheAlarmRows(t *testing.T) {
	const shared = "shared-alarm@test"
	tests := []struct {
		name   string
		alarms func() []string
	}{
		{name: "foreign fireable alarm", alarms: func() []string {
			return []string{valarmICS(shared, "DISPLAY", "-PT15M", "Reminder")}
		}},
		{name: "foreign sync-only alarm", alarms: func() []string {
			return []string{valarmICS(shared, "NONE", "-PT15M", "")}
		}},
		{name: "duplicate UID in one event", alarms: func() []string {
			alarm := valarmICS("dup-alarm@test", "DISPLAY", "-PT15M", "Reminder")
			return []string{alarm, alarm}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestApp(t)
			owner, roundTrip := twoCalendars(t, a)
			importFixture(t, a, owner, calendarICS(veventICS("owner@test", "Owner",
				valarmICS(shared, "DISPLAY", "-PT15M", "Owner alarm"))))
			importFixture(t, a, roundTrip, calendarICS(veventICS("round@test", "Round trip", tt.alarms()...)))
			wantIDs := alarmRowIDs(t, a, "round@test")
			_, wantUIDs := eventState(t, a, "round@test")

			s := importFixture(t, a, roundTrip, calendarICS(veventICS("round@test", "Round trip edited", tt.alarms()...)))
			if s.UpdatedEvents != 1 {
				t.Fatalf("updated events = %d, want 1", s.UpdatedEvents)
			}
			if got := alarmRowIDs(t, a, "round@test"); !slices.Equal(got, wantIDs) {
				t.Errorf("alarm row IDs = %v, want %v", got, wantIDs)
			}
			if _, got := eventState(t, a, "round@test"); !slices.Equal(got, wantUIDs) {
				t.Errorf("alarm UIDs = %v, want %v", got, wantUIDs)
			}
		})
	}
}

// TestImport_AlarmWithNoUIDDoesNotTakeTheRowOfAnotherAlarm covers a file
// that adds an alarm with no UID and edits an alarm with its own UID. The
// edited alarm must keep its row, its UID, and its alarm state.
func TestImport_AlarmWithNoUIDDoesNotTakeTheRowOfAnotherAlarm(t *testing.T) {
	a := newTestApp(t)
	_, roundTrip := twoCalendars(t, a)
	importFixture(t, a, roundTrip, calendarICS(veventICS("round@test", "Round trip",
		valarmICS("own@test", "DISPLAY", "-PT15M", "Reminder"))))
	ownRow := alarmRowIDs(t, a, "round@test")[0]

	importFixture(t, a, roundTrip, calendarICS(veventICS("round@test", "Round trip",
		valarmICS("", "DISPLAY", "-PT15M", "Reminder"),
		valarmICS("own@test", "DISPLAY", "-PT5M", "Reminder"))))
	ctx := context.Background()
	e, err := a.Events.GetByUID(ctx, "round@test")
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	alarms, err := a.Events.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatalf("ListAlarms: %v", err)
	}
	found := false
	for _, al := range alarms {
		if al.ID == ownRow {
			found = true
			if al.UID != "own@test" || al.TriggerValue != "-PT5M" {
				t.Errorf("row %d = UID %q trigger %q, want own@test and -PT5M", al.ID, al.UID, al.TriggerValue)
			}
		}
	}
	if !found {
		t.Errorf("alarm rows = %+v, want row %d to stay", alarms, ownRow)
	}
}
