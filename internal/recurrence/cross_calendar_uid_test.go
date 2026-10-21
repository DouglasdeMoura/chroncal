package recurrence

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestListExpandedEvents_SameUIDOnTwoCalendars is the issue #756 expansion
// contract. Two calendars can store the same UID. An override on one calendar
// must not hide the other calendar's occurrence.
func TestListExpandedEvents_SameUIDOnTwoCalendars(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()

	res, err := db.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Other')`)
	if err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	otherID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("calendar id: %v", err)
	}

	start := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Standup",
		StartTime:      start,
		EndTime:        end,
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create calendar 1 master: %v", err)
	}
	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Standup (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-06T09:00:00Z",
	}); err != nil {
		t.Fatalf("create calendar 1 override: %v", err)
	}

	if _, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID:            master.UID,
		CalendarID:     otherID,
		Title:          "Standup",
		StartTime:      start,
		EndTime:        end,
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	}); err != nil {
		t.Fatalf("create calendar 2 master: %v", err)
	}

	from := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	got, err := recurSvc.ListExpandedEvents(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedEvents: %v", err)
	}

	var cal1, cal2 []ExpandedEvent
	for _, ev := range got {
		switch ev.CalendarID {
		case 1:
			cal1 = append(cal1, ev)
		case otherID:
			cal2 = append(cal2, ev)
		}
	}
	if len(cal1) != 1 {
		t.Fatalf("calendar 1 instances = %d, want 1 (the moved override)", len(cal1))
	}
	if cal1[0].Title != "Standup (moved)" {
		t.Errorf("calendar 1 title = %q, want Standup (moved)", cal1[0].Title)
	}
	if len(cal2) != 1 {
		t.Fatalf("calendar 2 instances = %d, want 1 (the un-overridden Monday)", len(cal2))
	}
	if cal2[0].Title != "Standup" {
		t.Errorf("calendar 2 title = %q, want Standup", cal2[0].Title)
	}
	if !cal2[0].InstanceTime.Equal(start) {
		t.Errorf("calendar 2 instance = %s, want %s", cal2[0].InstanceTime, start)
	}
}

// TestListExpandedEvents_LegacyOverrideJoinsItsOwner: an override row in a
// calendar with no master of its UID belongs to the one live master (the
// owner rule of event_series_rows). The expansion shows it once in place
// of the master occurrence. A row of the owner calendar at the same
// occurrence wins over the legacy row.
func TestListExpandedEvents_LegacyOverrideJoinsItsOwner(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)
	ctx := context.Background()
	res, err := db.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Legacy')`)
	if err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	legacyCal, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("calendar id: %v", err)
	}
	start := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	master, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Standup", StartTime: start, EndTime: start.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	second := start.AddDate(0, 0, 7)
	legacy, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: legacyCal, Title: "Standup (legacy)",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
		RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	from, to := start, start.AddDate(0, 0, 14)
	atSecond := func() []ExpandedEvent {
		t.Helper()
		got, err := recurSvc.ListExpandedEvents(ctx, from, to)
		if err != nil {
			t.Fatalf("ListExpandedEvents: %v", err)
		}
		var out []ExpandedEvent
		for _, ev := range got {
			if ev.UID == master.UID && ev.StartTime.Before(second.AddDate(0, 0, 1)) && !ev.StartTime.Before(second) {
				out = append(out, ev)
			}
		}
		return out
	}
	if got := atSecond(); len(got) != 1 || got[0].ID != legacy.ID {
		t.Fatalf("rows at the second occurrence = %+v, want the legacy row %d once", got, legacy.ID)
	}
	own, err := eventsSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Standup (own)",
		StartTime: second.Add(2 * time.Hour), EndTime: second.Add(3 * time.Hour),
		RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert own override: %v", err)
	}
	if got := atSecond(); len(got) != 1 || got[0].ID != own.ID {
		t.Errorf("rows at the second occurrence = %+v, want the own row %d once", got, own.ID)
	}
}
