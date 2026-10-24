package icaltransfer_test

import (
	"context"
	"fmt"
	"testing"
)

const foreignAlarmICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:%s\r\nDTSTAMP:20261001T090000Z\r\nDTSTART:20261101T090000Z\r\n" +
	"DTEND:20261101T100000Z\r\nSUMMARY:%s\r\n" +
	"BEGIN:VALARM\r\nUID:shared-alarm@test\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\n" +
	"DESCRIPTION:%s\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// TestImport_UnchangedItemWithAForeignAlarmUIDKeepsItsSequence locks in the
// remaining case of issue #883 (issue #915). The file holds a VALARM whose
// UID belongs to an alarm of another calendar. Alarm UIDs are globally
// unique, so the store mints a local UID for this event. The next import of
// the same file must still count as unchanged and keep the SEQUENCE.
func TestImport_UnchangedItemWithAForeignAlarmUIDKeepsItsSequence(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	owner, err := a.Calendars.Create(ctx, "Imported", "", "")
	if err != nil {
		t.Fatalf("create owner calendar: %v", err)
	}
	roundTrip, err := a.Calendars.Create(ctx, "RoundTrip", "", "")
	if err != nil {
		t.Fatalf("create round-trip calendar: %v", err)
	}
	// The alarm UID below belongs to this event of the Imported calendar.
	importFixture(t, a, owner.ID, []byte(fmt.Sprintf(foreignAlarmICS, "owner-event@test", "Owner", "Owner alarm")))

	file := []byte(fmt.Sprintf(foreignAlarmICS, "round-event@test", "Round trip", "Round trip alarm"))
	importFixture(t, a, roundTrip.ID, file)
	stored, err := a.Events.GetByUID(ctx, "round-event@test")
	if err != nil {
		t.Fatalf("GetByUID round-event@test: %v", err)
	}
	want := stored.Sequence

	for run := range 2 {
		s := importFixture(t, a, roundTrip.ID, file)
		if got := s.UpdatedEvents; got != 0 {
			t.Errorf("run %d: updated events = %d, want 0", run, got)
		}
		if got := s.UnchangedEvents; got != 1 {
			t.Errorf("run %d: unchanged events = %d, want 1", run, got)
		}
		got, err := a.Events.GetByUID(ctx, "round-event@test")
		if err != nil {
			t.Fatalf("run %d: GetByUID round-event@test: %v", run, err)
		}
		if got.Sequence != want {
			t.Errorf("run %d: sequence = %d, want %d", run, got.Sequence, want)
		}
	}
}
