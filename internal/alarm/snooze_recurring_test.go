package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// firedInstanceAlarm fires every event alarm due at `now`, marks the one
// matching wantTrigger as fired, and returns its state ID. It fails the test
// if no such alarm is due.
func firedInstanceAlarm(t *testing.T, svc *Service, eventID int64, now, wantTrigger time.Time) int64 {
	t.Helper()
	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	for _, da := range due {
		if da.Event.ID == eventID && da.TriggerAt.Equal(wantTrigger) {
			stateID, err := svc.MarkFired(context.Background(), da)
			if err != nil {
				t.Fatalf("mark fired: %v", err)
			}
			return stateID
		}
	}
	t.Fatalf("no due alarm for event %d at trigger %v", eventID, wantTrigger)
	return 0
}

// TestComputeSnooze_RecurringInstanceTimes guards against issue #97: snoozing
// an alarm that fired for a later occurrence of a recurring event must use that
// occurrence's start/end, not the master row's first-occurrence times.
func TestComputeSnooze_RecurringInstanceTimes(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()
	eventsSvc := event.NewService(db, q)
	svc := NewService(db, q, eventsSvc, nil)

	// First occurrence a week before "now"; daily recurrence.
	base := time.Date(2026, 6, 18, 9, 0, 0, 0, time.UTC)
	evt, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Daily Standup",
		StartTime:      base,
		EndTime:        base.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY",
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := eventsSvc.ReplaceAlarms(ctx, evt.ID, []model.Alarm{
		{UID: "a1", Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Standup soon"},
	}); err != nil {
		t.Fatalf("add alarm: %v", err)
	}

	// Today's instance starts 09:00; the -15m alarm fires at 08:45.
	now := time.Date(2026, 6, 25, 8, 50, 0, 0, time.UTC)
	wantTrigger := time.Date(2026, 6, 25, 8, 45, 0, 0, time.UTC)
	stateID := firedInstanceAlarm(t, svc, evt.ID, now, wantTrigger)

	res, err := svc.ComputeSnooze(ctx, stateID, 5*time.Minute, now)
	if err != nil {
		// Bug: events.Get(masterID) returns the first occurrence (Jun 18),
		// which is before now, so ComputeSnooze rejects it as "already ended".
		t.Fatalf("ComputeSnooze rejected a live recurring instance: %v", err)
	}

	wantStart := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	if !res.EventStart.Equal(wantStart) {
		t.Errorf("EventStart = %v, want %v (instance start, not master first occurrence)", res.EventStart, wantStart)
	}
	wantEnd := wantStart.Add(30 * time.Minute)
	if !res.EventEnd.Equal(wantEnd) {
		t.Errorf("EventEnd = %v, want %v (instance end, not master first occurrence)", res.EventEnd, wantEnd)
	}
}

// TestSnoozeUntilStart_RecurringInstanceTimes covers the snooze-until-start
// path of issue #97: it must target the fired occurrence's start time.
func TestSnoozeUntilStart_RecurringInstanceTimes(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()
	eventsSvc := event.NewService(db, q)
	svc := NewService(db, q, eventsSvc, nil)

	base := time.Date(2026, 6, 18, 9, 0, 0, 0, time.UTC)
	evt, err := eventsSvc.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Daily Standup",
		StartTime:      base,
		EndTime:        base.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY",
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := eventsSvc.ReplaceAlarms(ctx, evt.ID, []model.Alarm{
		{UID: "a1", Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Standup soon"},
	}); err != nil {
		t.Fatalf("add alarm: %v", err)
	}

	now := time.Date(2026, 6, 25, 8, 50, 0, 0, time.UTC)
	wantTrigger := time.Date(2026, 6, 25, 8, 45, 0, 0, time.UTC)
	stateID := firedInstanceAlarm(t, svc, evt.ID, now, wantTrigger)

	res, err := svc.SnoozeUntilStart(ctx, stateID, now)
	if err != nil {
		t.Fatalf("SnoozeUntilStart rejected a live recurring instance: %v", err)
	}
	wantStart := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	if !res.Until.Equal(wantStart) {
		t.Errorf("Until = %v, want %v (instance start)", res.Until, wantStart)
	}
}
