package event

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	return NewService(db, q)
}

func createEvent(t *testing.T, svc *Service) Event {
	t.Helper()
	e, err := svc.Create(context.Background(), CreateParams{
		CalendarID: 1,
		Title:      "Test Event",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return e
}

func TestEventService_Create(t *testing.T) {
	svc := newTestService(t)
	e := createEvent(t, svc)

	if e.ID == 0 {
		t.Error("ID is 0")
	}
	if e.UID == "" {
		t.Error("UID is empty")
	}
	if e.Title != "Test Event" {
		t.Errorf("Title = %q, want %q", e.Title, "Test Event")
	}
	if e.Status != "CONFIRMED" {
		t.Errorf("Status = %q, want %q", e.Status, "CONFIRMED")
	}
	if e.Transp != "OPAQUE" {
		t.Errorf("Transp = %q, want %q", e.Transp, "OPAQUE")
	}
	if e.Class != "PUBLIC" {
		t.Errorf("Class = %q, want %q", e.Class, "PUBLIC")
	}
}

func TestEventService_Get(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createEvent(t, svc)

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Title != created.Title {
		t.Errorf("Title = %q, want %q", got.Title, created.Title)
	}

	_, err = svc.Get(ctx, 999)
	if err == nil {
		t.Error("Get(999) expected error")
	}
}

func TestEventService_ListByDateRange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	createEvent(t, svc) // April 1

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	events, err := svc.ListByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListByDateRange error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("got %d events, want 1", len(events))
	}

	// Out of range
	from2 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to2 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	events2, _ := svc.ListByDateRange(ctx, from2, to2)
	if len(events2) != 0 {
		t.Errorf("out-of-range returned %d events, want 0", len(events2))
	}
}

func TestEventService_ListByDateRange_MultiDayOverlap(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Multi-day event: starts March 28, ends April 2.
	_, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Multi-Day Conference",
		StartTime:  time.Date(2026, 3, 28, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 2, 17, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create multi-day: %v", err)
	}

	// Single-day event inside the range.
	_, err = svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Normal Meeting",
		StartTime:  time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create normal: %v", err)
	}

	// Event entirely before the range.
	_, err = svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Past Event",
		StartTime:  time.Date(2026, 3, 25, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 3, 26, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create past: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)

	events, err := svc.ListByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListByDateRange: %v", err)
	}

	if len(events) != 2 {
		for i, e := range events {
			t.Logf("  events[%d]: %s start=%v end=%v", i, e.Title, e.StartTime, e.EndTime)
		}
		t.Fatalf("got %d events, want 2", len(events))
	}

	titles := map[string]bool{}
	for _, e := range events {
		titles[e.Title] = true
	}
	if !titles["Multi-Day Conference"] {
		t.Error("multi-day event not found in results")
	}
	if !titles["Normal Meeting"] {
		t.Error("normal meeting not found in results")
	}
}

func TestEventService_ListByDateRange_BoundaryExclusion(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Event ending exactly at window start (end_time == from): should NOT match.
	_, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Ends At From",
		StartTime:  time.Date(2026, 3, 29, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Event starting exactly at window end (start_time == to): should NOT match.
	_, err = svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Starts At To",
		StartTime:  time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 5, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	from := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)

	events, err := svc.ListByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListByDateRange: %v", err)
	}

	if len(events) != 0 {
		for i, e := range events {
			t.Logf("  events[%d]: %s start=%v end=%v", i, e.Title, e.StartTime, e.EndTime)
		}
		t.Fatalf("got %d events, want 0 (boundary events should be excluded)", len(events))
	}
}

func TestEventService_ListByCalendarAndDateRange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	createEvent(t, svc)

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	events, _ := svc.ListByCalendarAndDateRange(ctx, 1, from, to)
	if len(events) != 1 {
		t.Errorf("calendar 1: got %d, want 1", len(events))
	}

	events2, _ := svc.ListByCalendarAndDateRange(ctx, 999, from, to)
	if len(events2) != 0 {
		t.Errorf("calendar 999: got %d, want 0", len(events2))
	}
}

func TestEventService_Update(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createEvent(t, svc)

	updated, err := svc.Update(ctx, created.ID, UpdateParams{
		Title:      "Updated Title",
		StartTime:  created.StartTime,
		EndTime:    created.EndTime,
		CalendarID: 1,
		Status:     "TENTATIVE",
		Transp:     "TRANSPARENT",
		Class:      "PRIVATE",
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated.Title != "Updated Title" {
		t.Errorf("Title = %q, want %q", updated.Title, "Updated Title")
	}
	if updated.Sequence != 1 {
		t.Errorf("Sequence = %d, want 1 (auto-incremented)", updated.Sequence)
	}
	if updated.Status != "TENTATIVE" {
		t.Errorf("Status = %q, want %q", updated.Status, "TENTATIVE")
	}
}

func TestEventService_UpsertByUID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	p := UpsertParams{
		UID:        "test-upsert-uid",
		CalendarID: 1,
		Title:      "Original",
		StartTime:  time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	}

	first, err := svc.UpsertByUID(ctx, p)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.Title != "Original" {
		t.Errorf("first Title = %q", first.Title)
	}

	p.Title = "Updated"
	second, err := svc.UpsertByUID(ctx, p)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("upsert created new row: ID %d != %d", second.ID, first.ID)
	}
	if second.Title != "Updated" {
		t.Errorf("second Title = %q, want %q", second.Title, "Updated")
	}
}

func TestEventService_Delete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createEvent(t, svc)

	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	_, err := svc.Get(ctx, created.ID)
	if err == nil {
		t.Error("Get after Delete expected error")
	}
}

func TestEventService_Alarms(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	// Initially empty
	alarms, _ := svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 0 {
		t.Fatalf("initial alarms = %d, want 0", len(alarms))
	}

	// Replace with 2 alarms
	err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 min before"},
		{Action: "EMAIL", TriggerValue: "-PT1H", Description: "1 hour before"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms error: %v", err)
	}

	alarms, _ = svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 2 {
		t.Fatalf("after replace: alarms = %d, want 2", len(alarms))
	}
	if alarms[0].Action != "DISPLAY" {
		t.Errorf("alarm[0].Action = %q, want %q", alarms[0].Action, "DISPLAY")
	}

	// Replace again (should delete old ones)
	svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "AUDIO", TriggerValue: "-PT5M"},
	})
	alarms, _ = svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 1 {
		t.Errorf("after second replace: alarms = %d, want 1", len(alarms))
	}
}

func TestEventService_ReplaceAlarms_UIDMatchPreservesRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{UID: "alarm-uid-1", Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms error: %v", err)
	}
	alarms, _ := svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 1 {
		t.Fatalf("alarms = %d, want 1", len(alarms))
	}
	origID := alarms[0].ID

	// Same UID, changed trigger: must update the row in place so alarm_state
	// rows keyed to its ID survive, not delete + recreate.
	err = svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{UID: "alarm-uid-1", Action: "DISPLAY", TriggerValue: "-PT30M"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms (trigger change) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 1 {
		t.Fatalf("after trigger change: alarms = %d, want 1", len(alarms))
	}
	if alarms[0].ID != origID {
		t.Errorf("alarm row ID changed %d -> %d; UID-matched edit must update in place", origID, alarms[0].ID)
	}
	if alarms[0].TriggerValue != "-PT30M" {
		t.Errorf("TriggerValue = %q, want %q", alarms[0].TriggerValue, "-PT30M")
	}

	// A different UID is a genuinely new alarm: row must be replaced.
	err = svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{UID: "alarm-uid-2", Action: "DISPLAY", TriggerValue: "-PT45M"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms (new uid) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 1 {
		t.Fatalf("after new uid: alarms = %d, want 1", len(alarms))
	}
	if alarms[0].ID == origID {
		t.Errorf("alarm row ID %d reused for different UID; want a new row", origID)
	}
}

func TestEventService_Attendees(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	err := svc.ReplaceAttendees(ctx, e.ID, []model.Attendee{
		{Email: "org@example.com", Name: "Organizer", RSVPStatus: "ACCEPTED", Role: "CHAIR", Organizer: true},
		{Email: "user@example.com", Name: "User", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"},
	})
	if err != nil {
		t.Fatalf("ReplaceAttendees error: %v", err)
	}

	attendees, _ := svc.ListAttendees(ctx, e.ID)
	if len(attendees) != 2 {
		t.Fatalf("attendees = %d, want 2", len(attendees))
	}
	// Organizer sorted first (ORDER BY organizer DESC)
	if !attendees[0].Organizer {
		t.Error("first attendee should be organizer")
	}
}

func TestEventService_ListOverridesByUID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Master event
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "recurring-uid", CalendarID: 1, Title: "Weekly Meeting",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})

	// Override instance
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "recurring-uid", CalendarID: 1, Title: "Weekly Meeting (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})

	overrides, err := svc.ListOverridesByUID(ctx, "recurring-uid")
	if err != nil {
		t.Fatalf("ListOverridesByUID error: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("overrides = %d, want 1", len(overrides))
	}
	if overrides[0].Title != "Weekly Meeting (moved)" {
		t.Errorf("override title = %q", overrides[0].Title)
	}
}

func TestEventService_GetByUIDAndRecurrenceID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Master
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "rec-uid", CalendarID: 1, Title: "Weekly",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})

	// Override
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "rec-uid", CalendarID: 1, Title: "Weekly (rescheduled)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})

	// GetByUID returns master only.
	master, err := svc.GetByUID(ctx, "rec-uid")
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	if master.Title != "Weekly" {
		t.Errorf("master.Title = %q, want %q", master.Title, "Weekly")
	}

	// GetByUIDAndRecurrenceID returns the override.
	override, err := svc.GetByUIDAndRecurrenceID(ctx, "rec-uid", "2026-04-08T10:00:00Z")
	if err != nil {
		t.Fatalf("GetByUIDAndRecurrenceID: %v", err)
	}
	if override.Title != "Weekly (rescheduled)" {
		t.Errorf("override.Title = %q, want %q", override.Title, "Weekly (rescheduled)")
	}

	// Non-existent recurrence-id returns error.
	_, err = svc.GetByUIDAndRecurrenceID(ctx, "rec-uid", "2099-01-01T00:00:00Z")
	if err == nil {
		t.Error("expected error for non-existent recurrence-id, got nil")
	}
}

func TestDelete_MasterWithOverridesRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-master", CalendarID: 1, Title: "Weekly",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-master", CalendarID: 1, Title: "Weekly (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})

	master, _ := svc.GetByUID(ctx, "del-master")
	err := svc.Delete(ctx, master.ID)
	if !errors.Is(err, ErrHasOverrides) {
		t.Fatalf("Delete master with overrides: got %v, want ErrHasOverrides", err)
	}
}

func TestDelete_MasterNoOverridesSucceeds(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-solo", CalendarID: 1, Title: "Solo Recurring",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY",
	})

	master, _ := svc.GetByUID(ctx, "del-solo")
	if err := svc.Delete(ctx, master.ID); err != nil {
		t.Fatalf("Delete solo master: %v", err)
	}

	_, err := svc.Get(ctx, master.ID)
	if err == nil {
		t.Error("expected error after deletion, got nil")
	}
}

func TestDelete_OverrideAddsEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "exd-test", CalendarID: 1, Title: "Weekly",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "exd-test", CalendarID: 1, Title: "Weekly (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})

	override, _ := svc.GetByUIDAndRecurrenceID(ctx, "exd-test", "2026-04-08T10:00:00Z")
	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete override: %v", err)
	}

	master, err := svc.GetByUID(ctx, "exd-test")
	if err != nil {
		t.Fatalf("Get master after override delete: %v", err)
	}
	exdates := master.ParseExDates()
	if len(exdates) != 1 {
		t.Fatalf("exdates count = %d, want 1", len(exdates))
	}
	want := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	if !exdates[0].Equal(want) {
		t.Errorf("exdate = %v, want %v", exdates[0], want)
	}
}

func TestDelete_OverridePreservesExistingEXDATEs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "pre-exd", CalendarID: 1, Title: "Weekly",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY",
		ExDates:        "2026-04-15T10:00:00Z",
	})
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "pre-exd", CalendarID: 1, Title: "Weekly (moved)",
		StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})

	override, _ := svc.GetByUIDAndRecurrenceID(ctx, "pre-exd", "2026-04-08T10:00:00Z")
	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete override: %v", err)
	}

	master, _ := svc.GetByUID(ctx, "pre-exd")
	exdates := master.ParseExDates()
	if len(exdates) != 2 {
		t.Fatalf("exdates count = %d, want 2 (existing + new)", len(exdates))
	}
}

func TestDelete_AllDayOverrideAddsDateOnlyEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "allday-exd", CalendarID: 1, Title: "Daily Standup",
		StartTime:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		AllDay:         true,
		RecurrenceRule: "FREQ=DAILY",
	})
	// Override with date-only RecurrenceID (all-day event).
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "allday-exd", CalendarID: 1, Title: "Standup (cancelled)",
		StartTime:    time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC),
		AllDay:       true,
		RecurrenceID: "2026-04-08",
	})

	override, _ := svc.GetByUIDAndRecurrenceID(ctx, "allday-exd", "2026-04-08")
	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete all-day override: %v", err)
	}

	master, _ := svc.GetByUID(ctx, "allday-exd")
	// The EXDATE should be in date-only format matching the all-day event.
	exdates := master.ParseExDates()
	if len(exdates) != 1 {
		t.Fatalf("exdates count = %d, want 1", len(exdates))
	}
	// Verify it's midnight in Local (date-only format).
	ex := exdates[0]
	if ex.Location() != time.Local {
		t.Errorf("exdate location = %v, want time.Local (date-only)", ex.Location())
	}
	if ex.Hour() != 0 || ex.Minute() != 0 {
		t.Errorf("exdate time = %v, want midnight", ex)
	}
}

func TestDeleteSeries_CascadesAll(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "series-del", CalendarID: 1, Title: "Weekly",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	for _, recID := range []string{"2026-04-08T10:00:00Z", "2026-04-15T10:00:00Z", "2026-04-22T10:00:00Z"} {
		svc.UpsertByUID(ctx, UpsertParams{
			UID: "series-del", CalendarID: 1, Title: "Override",
			StartTime:    time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
			EndTime:      time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
			RecurrenceID: recID,
		})
	}

	if err := svc.DeleteSeries(ctx, "series-del"); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}

	_, err := svc.GetByUID(ctx, "series-del")
	if err == nil {
		t.Error("master should be deleted")
	}
	overrides, _ := svc.ListOverridesByUID(ctx, "series-del")
	if len(overrides) != 0 {
		t.Errorf("overrides remaining = %d, want 0", len(overrides))
	}
}

func TestEventService_CreateDefaults(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	e, _ := svc.Create(ctx, CreateParams{
		CalendarID: 1, Title: "No Defaults Set",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		// Status, Transp, Class left empty
	})
	if e.Status != "CONFIRMED" {
		t.Errorf("default Status = %q, want CONFIRMED", e.Status)
	}
	if e.Transp != "OPAQUE" {
		t.Errorf("default Transp = %q, want OPAQUE", e.Transp)
	}
	if e.Class != "PUBLIC" {
		t.Errorf("default Class = %q, want PUBLIC", e.Class)
	}
}

func TestEventService_CreateUppercasesEnums(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	e, err := svc.Create(ctx, CreateParams{
		CalendarID: 1, Title: "Lowercase Enums",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		Status:    "tentative",
		Transp:    "transparent",
		Class:     "private",
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if e.Status != "TENTATIVE" {
		t.Errorf("Status = %q, want TENTATIVE", e.Status)
	}
	if e.Transp != "TRANSPARENT" {
		t.Errorf("Transp = %q, want TRANSPARENT", e.Transp)
	}
	if e.Class != "PRIVATE" {
		t.Errorf("Class = %q, want PRIVATE", e.Class)
	}
}

func TestEventService_UpdateUppercasesEnums(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createEvent(t, svc)

	updated, err := svc.Update(ctx, created.ID, UpdateParams{
		Title:      created.Title,
		StartTime:  created.StartTime,
		EndTime:    created.EndTime,
		CalendarID: 1,
		Status:     "cancelled",
		Transp:     "transparent",
		Class:      "confidential",
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated.Status != "CANCELLED" {
		t.Errorf("Status = %q, want CANCELLED", updated.Status)
	}
	if updated.Transp != "TRANSPARENT" {
		t.Errorf("Transp = %q, want TRANSPARENT", updated.Transp)
	}
	if updated.Class != "CONFIDENTIAL" {
		t.Errorf("Class = %q, want CONFIDENTIAL", updated.Class)
	}
}

func TestEventService_ListAlarmsByEventIDs_Correctness(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create 3 events with varying numbers of alarms
	events := make([]Event, 3)
	alarmsPerEvent := [][]model.Alarm{
		// Event 1: 3 alarms
		{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 min"},
			{Action: "DISPLAY", TriggerValue: "-PT5M", Description: "5 min"},
			{Action: "DISPLAY", TriggerValue: "PT0H", Description: "at start"},
		},
		// Event 2: 1 alarm
		{
			{Action: "DISPLAY", TriggerValue: "-PT30M", Description: "30 min"},
		},
		// Event 3: no alarms (empty slice)
		{},
	}

	for i, alarms := range alarmsPerEvent {
		e, err := svc.Create(ctx, CreateParams{
			CalendarID: 1,
			Title:      "Event " + string(rune('1'+i)),
			StartTime:  time.Date(2026, 4, 1+i, 14, 0, 0, 0, time.UTC),
			EndTime:    time.Date(2026, 4, 1+i, 15, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("create event %d: %v", i, err)
		}
		events[i] = e
		if len(alarms) > 0 {
			if err := svc.ReplaceAlarms(ctx, e.ID, alarms); err != nil {
				t.Fatalf("replace alarms event %d: %v", i, err)
			}
		}
	}

	// Fetch alarms individually (old N+1 pattern)
	individualMap := make(map[int64][]model.Alarm)
	for _, e := range events {
		alarms, err := svc.ListAlarms(ctx, e.ID)
		if err != nil {
			t.Fatalf("ListAlarms(%d): %v", e.ID, err)
		}
		individualMap[e.ID] = alarms
	}

	// Fetch alarms in batch (new optimized pattern)
	eventIDs := []int64{events[0].ID, events[1].ID, events[2].ID}
	batchMap, err := svc.ListAlarmsByEventIDs(ctx, eventIDs)
	if err != nil {
		t.Fatalf("ListAlarmsByEventIDs: %v", err)
	}

	// Verify results are identical
	for _, e := range events {
		individual := individualMap[e.ID]
		batch := batchMap[e.ID]

		if len(individual) != len(batch) {
			t.Errorf("event %d: individual got %d alarms, batch got %d",
				e.ID, len(individual), len(batch))
			continue
		}

		for i := range individual {
			if individual[i].ID != batch[i].ID {
				t.Errorf("event %d alarm %d: ID = %d (batch), want %d (individual)",
					e.ID, i, batch[i].ID, individual[i].ID)
			}
			if individual[i].Action != batch[i].Action {
				t.Errorf("event %d alarm %d: Action = %q (batch), want %q",
					e.ID, i, batch[i].Action, individual[i].Action)
			}
			if individual[i].TriggerValue != batch[i].TriggerValue {
				t.Errorf("event %d alarm %d: TriggerValue = %q (batch), want %q",
					e.ID, i, batch[i].TriggerValue, individual[i].TriggerValue)
			}
			if individual[i].Description != batch[i].Description {
				t.Errorf("event %d alarm %d: Description = %q (batch), want %q",
					e.ID, i, batch[i].Description, individual[i].Description)
			}
		}
	}

	// Test empty input
	emptyMap, err := svc.ListAlarmsByEventIDs(ctx, []int64{})
	if err != nil {
		t.Errorf("empty eventIDs: got error %v", err)
	}
	if emptyMap != nil {
		t.Errorf("empty eventIDs: got non-nil map %v", emptyMap)
	}

	// Test non-existent event IDs
	nonExistent := []int64{9999, 9998}
	nonExistentMap, err := svc.ListAlarmsByEventIDs(ctx, nonExistent)
	if err != nil {
		t.Errorf("non-existent eventIDs: got error %v", err)
	}
	if len(nonExistentMap) != 0 {
		t.Errorf("non-existent eventIDs: got map with %d entries, want 0", len(nonExistentMap))
	}
}
