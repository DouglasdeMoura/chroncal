package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/testutil"
)

func newTestServices(t *testing.T) (*Service, *event.Service) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	alarmSvc := NewService(db, q, evtSvc)
	return alarmSvc, evtSvc
}

func TestCheck_FiresDueAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Create event starting in 10 minutes with a 15-min-before alarm
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Meeting",
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

	// Check: alarm trigger is 15 min before start = 5 min ago = should fire
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due alarms, want 1", len(due))
	}
	if due[0].Event.Title != "Meeting" {
		t.Errorf("event title = %q, want %q", due[0].Event.Title, "Meeting")
	}
	if due[0].Alarm.Action != "DISPLAY" {
		t.Errorf("alarm action = %q, want %q", due[0].Alarm.Action, "DISPLAY")
	}
}

func TestCheck_SkipsAlreadyFired(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Meeting",
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

	// First check fires
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("first check: got %d, want 1", len(due))
	}

	// Mark as fired
	err = svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatal(err)
	}

	// Second check should skip it
	due, err = svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("second check: got %d, want 0", len(due))
	}
}

func TestCheck_SkipsFutureAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event in 2 hours with 15-min alarm = trigger is 1h45m from now
	start := time.Now().Add(2 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Later Meeting",
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

	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("got %d due alarms, want 0", len(due))
	}
}

func TestCheck_SkipsStaleAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event was 2 days ago -- alarm is stale beyond the 24h threshold
	start := time.Now().Add(-48 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Old Meeting",
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

	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("got %d due alarms, want 0 (stale)", len(due))
	}
}

func TestCheck_RefiresSnoozedAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event starts in 10 minutes; alarm at -PT15M triggers 5 min ago.
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Snoozed Refire",
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

	// Fire the alarm
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("step 1: got %d, want 1", len(due))
	}
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}

	// Snooze for 1 second in the past (already expired)
	pending, _ := svc.ListPending(ctx)
	pastSnooze := time.Now().Add(-1 * time.Second)
	if err := svc.Snooze(ctx, pending[0].ID, pastSnooze); err != nil {
		t.Fatal(err)
	}

	// Check should re-fire the snoozed alarm
	due, err = svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("step 3: got %d, want 1 (snoozed refire)", len(due))
	}
	if due[0].StateID == 0 {
		t.Error("re-fired alarm should have non-zero StateID")
	}

	// MarkRefired clears snoozed_to
	if err := svc.MarkRefired(ctx, due[0].StateID); err != nil {
		t.Fatal(err)
	}

	// Check again: no expired snoozes, no fresh alarms (already has state row)
	due, err = svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("step 4: got %d, want 0 (refired, no more snooze)", len(due))
	}
}

func TestCheck_SkipsActiveSnoozedAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Active Snooze",
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

	// Fire and snooze into the future
	due, _ := svc.Check(ctx, time.Now())
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)
	futureSnooze := time.Now().Add(1 * time.Hour)
	if err := svc.Snooze(ctx, pending[0].ID, futureSnooze); err != nil {
		t.Fatal(err)
	}

	// Check: alarm is snoozed into the future, should NOT re-fire
	due, err = svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("got %d, want 0 (snooze not expired yet)", len(due))
	}
}

func TestCheck_RelatedEnd(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event ends in 10 minutes, alarm is 15 min before END
	start := time.Now().Add(-50 * time.Minute)
	end := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Ending Soon",
		StartTime:  start,
		EndTime:    end,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "END"},
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due alarms, want 1", len(due))
	}
}
