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

	// 7. Wait for snooze to "expire" by snoozing into the past
	expiredSnooze := time.Now().Add(-1 * time.Second)
	err = svc.Snooze(ctx, pending[0].ID, expiredSnooze)
	if err != nil {
		t.Fatal(err)
	}

	// 8. Check again -- snoozed alarm should re-fire
	due, err = svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("step 8: got %d due alarms, want 1 (snoozed refire)", len(due))
	}
	if due[0].StateID == 0 {
		t.Fatal("step 8: re-fired alarm should have non-zero StateID")
	}

	// 9. MarkRefired and dismiss
	err = svc.MarkRefired(ctx, due[0].StateID)
	if err != nil {
		t.Fatal(err)
	}
	err = svc.Dismiss(ctx, due[0].StateID)
	if err != nil {
		t.Fatal(err)
	}

	// 10. ListPending should return 0
	pending, err = svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("step 10: got %d pending alarms, want 0", len(pending))
	}
}

func TestSnoozeSurvivesEventUpdate(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc)
	ctx := context.Background()

	// 1. Create event with alarm
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Snooze Survives",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Fire the alarm
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("want 1 due alarm, got %d", len(due))
	}
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}

	// 3. Snooze the alarm
	pending, err := svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending alarm, got %d", len(pending))
	}
	stateID := pending[0].ID
	snoozeUntil := time.Now().Add(30 * time.Minute)
	if err := svc.Snooze(ctx, stateID, snoozeUntil); err != nil {
		t.Fatal(err)
	}

	// 4. Update the event title (this calls ReplaceAlarms with same alarms)
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 5. Verify snooze state survived the update
	pending, err = svc.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending alarm after update, got %d (snooze state was lost!)", len(pending))
	}
	if pending[0].SnoozedTo.String == "" {
		t.Error("snooze-until time was lost after event update")
	}
}

func TestReplaceAlarms_PreservesUnchangedDeletesRemoved(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	ctx := context.Background()

	// Create event with two alarms
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Merge Test",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
		{Action: "DISPLAY", TriggerValue: "-PT5M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Get original alarm IDs
	alarms1, err := evtSvc.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(alarms1) != 2 {
		t.Fatalf("want 2 alarms, got %d", len(alarms1))
	}
	id15m := alarms1[0].ID
	id5m := alarms1[1].ID

	// Replace with only the -PT15M alarm (remove -PT5M, keep -PT15M)
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify: -PT15M kept its ID, -PT5M gone
	alarms2, err := evtSvc.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(alarms2) != 1 {
		t.Fatalf("want 1 alarm, got %d", len(alarms2))
	}
	if alarms2[0].ID != id15m {
		t.Errorf("alarm ID changed: got %d, want %d (row was not preserved)", alarms2[0].ID, id15m)
	}
	_ = id5m // was deleted
}

func TestReplaceAlarms_AssignsUID(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	ctx := context.Background()

	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "UID Test",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatal(err)
	}

	alarms, err := evtSvc.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(alarms) != 1 {
		t.Fatalf("want 1 alarm, got %d", len(alarms))
	}
	if alarms[0].UID == "" {
		t.Error("alarm should have a UID assigned")
	}
}
