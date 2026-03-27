package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/testutil"
)

func TestAlarmLifecycle(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc)
	ctx := context.Background()

	// 1. Create an event starting in 10 minutes with two alarms:
	//    DISPLAY at -PT15M and AUDIO at -PT5M
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Lifecycle Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 min reminder"},
		{Action: "AUDIO", TriggerValue: "-PT5M", Description: "5 min reminder"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Check -- only the -PT15M alarm should be due (triggered 5 min ago),
	//    the -PT5M is still 5 min in the future
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("step 2: got %d due alarms, want 1", len(due))
	}
	if due[0].Alarm.Action != "DISPLAY" {
		t.Errorf("step 2: alarm action = %q, want %q", due[0].Alarm.Action, "DISPLAY")
	}

	// 3. MarkFired on the due alarm
	err = svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatal(err)
	}

	// 4. Check again -- nothing due
	due, err = svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("step 4: got %d due alarms, want 0", len(due))
	}

	// 5. ListPending -- should return 1 (fired but not acked)
	pending, err := svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("step 5: got %d pending alarms, want 1", len(pending))
	}

	// 6. Dismiss it
	err = svc.Dismiss(ctx, pending[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	// 7. ListPending -- should return 0
	pending, err = svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("step 7: got %d pending alarms, want 0", len(pending))
	}
}

func TestAlarmLifecycle_Snooze(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc)
	ctx := context.Background()

	// 1. Create event starting in 10 minutes with DISPLAY alarm at -PT15M
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Snooze Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 min reminder"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Check -- fires
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("step 2: got %d due alarms, want 1", len(due))
	}

	// 3. MarkFired
	err = svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatal(err)
	}

	// 4. ListPending -- 1 pending
	pending, err := svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("step 4: got %d pending alarms, want 1", len(pending))
	}

	// 5. Snooze for 10 minutes
	snoozeUntil := time.Now().Add(10 * time.Minute)
	err = svc.Snooze(ctx, pending[0].ID, snoozeUntil)
	if err != nil {
		t.Fatal(err)
	}

	// 6. Verify snoozed_to is set on the pending alarm
	pending, err = svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("step 6: got %d pending alarms, want 1", len(pending))
	}
	if !pending[0].SnoozedTo.Valid {
		t.Fatal("step 6: SnoozedTo should be set after snooze, but Valid is false")
	}
}
