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

func TestComputeSnooze_CapsAtEventEnd(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event starts in 10 min, ends in 70 min. Alarm at -PT15M (fires 5 min ago).
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Cap Test",
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

	// Fire and mark
	due, _ := svc.Check(ctx, time.Now())
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)
	stateID := pending[0].ID

	// Snooze for 24 hours -- should be capped at event end (~70 min from now)
	res, err := svc.ComputeSnooze(ctx, stateID, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Capped {
		t.Error("expected Capped=true when snooze exceeds event end")
	}
	if !res.PastStart {
		t.Error("expected PastStart=true when capped to event end (which is after start)")
	}
	// The capped time should be approximately equal to event end
	if diff := res.Until.Sub(start.Add(time.Hour)); diff < -time.Second || diff > time.Second {
		t.Errorf("capped until=%v, want ~%v (event end)", res.Until, start.Add(time.Hour))
	}
}

func TestComputeSnooze_WarnsPastStart(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event starts in 5 min, ends in 65 min. Alarm at -PT15M (fires 10 min ago).
	start := time.Now().Add(5 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "PastStart Test",
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

	due, _ := svc.Check(ctx, time.Now())
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)
	stateID := pending[0].ID

	// Snooze for 10 minutes -- fires 5 min after event starts
	res, err := svc.ComputeSnooze(ctx, stateID, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !res.PastStart {
		t.Error("expected PastStart=true when snooze fires after event start")
	}
	if res.Capped {
		t.Error("expected Capped=false when snooze is within event end")
	}
}

func TestSnoozeUntilStart(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event starts in 10 min. Alarm at -PT15M fires 5 min ago.
	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "UntilStart Test",
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

	due, _ := svc.Check(ctx, time.Now())
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)
	stateID := pending[0].ID

	res, err := svc.SnoozeUntilStart(ctx, stateID)
	if err != nil {
		t.Fatal(err)
	}
	// Until should be approximately event start
	if diff := res.Until.Sub(start); diff < -time.Second || diff > time.Second {
		t.Errorf("until=%v, want ~%v (event start)", res.Until, start)
	}
}

func TestSnoozeUntilStart_RejectsStartedEvent(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event started 10 min ago (alarm at -PT15M triggers 25 min ago, still within 24h stale threshold)
	start := time.Now().Add(-10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Already Started",
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

	// The alarm trigger was 25 min ago -- Check() should fire it
	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due, want 1", len(due))
	}
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}

	pending, _ := svc.ListPending(ctx)
	if len(pending) == 0 {
		t.Fatal("expected 1 pending alarm")
	}

	_, err = svc.SnoozeUntilStart(ctx, pending[0].ID)
	if err == nil {
		t.Error("expected error when event has already started")
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

func TestCheck_AbsoluteTriggerUTC(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event starts in 2 hours. Absolute trigger is 5 minutes ago.
	start := time.Now().Add(2 * time.Hour)
	triggerTime := time.Now().Add(-5 * time.Minute).UTC()
	triggerStr := triggerTime.Format("20060102T150405Z")

	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Absolute UTC",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: triggerStr, Description: "abs trigger"},
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("absolute UTC trigger: got %d due alarms, want 1", len(due))
	}
	if due[0].Event.Title != "Absolute UTC" {
		t.Errorf("event title = %q, want %q", due[0].Event.Title, "Absolute UTC")
	}
}

func TestCheck_AbsoluteTriggerFuture(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event starts in 2 hours. Absolute trigger is 1 hour from now (future).
	start := time.Now().Add(2 * time.Hour)
	triggerTime := time.Now().Add(1 * time.Hour).UTC()
	triggerStr := triggerTime.Format("20060102T150405Z")

	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Absolute Future",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: triggerStr, Description: "future abs"},
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("absolute future trigger: got %d due alarms, want 0", len(due))
	}
}

func TestComputeTriggerTime_Absolute(t *testing.T) {
	evt := event.Event{
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name    string
		trigger string
		tz      string
		want    time.Time
		wantErr bool
	}{
		{
			name:    "iCal UTC",
			trigger: "20260401T170000Z",
			want:    time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC),
		},
		{
			name:    "iCal floating with timezone",
			trigger: "20260401T120000",
			tz:      "America/New_York",
			want:    time.Date(2026, 4, 1, 12, 0, 0, 0, mustLoadLocation("America/New_York")),
		},
		{
			name:    "iCal floating no timezone",
			trigger: "20260401T120000",
			want:    time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			name:    "RFC 3339 legacy",
			trigger: "2026-04-01T17:00:00Z",
			want:    time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC),
		},
		{
			name:    "duration still works",
			trigger: "-PT15M",
			want:    time.Date(2026, 4, 1, 13, 45, 0, 0, time.UTC),
		},
		{
			name:    "empty trigger errors",
			trigger: "",
			wantErr: true,
		},
		{
			name:    "garbage errors",
			trigger: "not-a-trigger",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := evt
			e.Timezone = tt.tz
			a := model.Alarm{TriggerValue: tt.trigger, Related: "START"}
			got, err := computeTriggerTime(e, a)
			if (err != nil) != tt.wantErr {
				t.Fatalf("computeTriggerTime() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !got.Equal(tt.want) {
				t.Errorf("computeTriggerTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestComputeTriggerTime_DST(t *testing.T) {
	nyc := mustLoadLocation("America/New_York")

	// March 8 2026: DST starts in NYC (clocks spring forward at 2:00 AM).
	// Event at 14:00 EDT (UTC-4) = 18:00 UTC.
	// A -P1D alarm should fire at 14:00 EST (UTC-5) on March 7 = 19:00 UTC.
	// Without the timezone fix, it would fire at 18:00 UTC (1 hour early).
	eventStart := time.Date(2026, 3, 8, 14, 0, 0, 0, nyc)

	// Simulate DB round-trip: store as RFC 3339, parse back.
	// This produces a fixed-offset time (zone "-04:00"), NOT location-aware.
	storedRFC3339 := eventStart.Format(time.RFC3339)
	parsedBack, _ := time.Parse(time.RFC3339, storedRFC3339)
	if parsedBack.Location().String() == "America/New_York" {
		t.Fatal("expected fixed-offset zone after RFC 3339 round-trip, got named location")
	}

	evt := event.Event{
		StartTime: parsedBack,
		EndTime:   parsedBack.Add(time.Hour),
		Timezone:  "America/New_York",
	}

	// -P1D: one day before, should be 14:00 EST on March 7
	a := model.Alarm{TriggerValue: "-P1D", Related: "START"}
	got, err := computeTriggerTime(evt, a)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 7, 14, 0, 0, 0, nyc) // 14:00 EST = 19:00 UTC
	if !got.Equal(want) {
		t.Errorf("DST -P1D: got %v (%s UTC), want %v (%s UTC)",
			got, got.UTC().Format("15:04"), want, want.UTC().Format("15:04"))
	}

	// -PT1H: one hour before, should be 13:00 EDT = 17:00 UTC (DST irrelevant for hours)
	a2 := model.Alarm{TriggerValue: "-PT1H", Related: "START"}
	got2, err := computeTriggerTime(evt, a2)
	if err != nil {
		t.Fatal(err)
	}
	want2 := eventStart.Add(-time.Hour) // 17:00 UTC
	if !got2.Equal(want2) {
		t.Errorf("DST -PT1H: got %v (%s UTC), want %v (%s UTC)",
			got2, got2.UTC().Format("15:04"), want2, want2.UTC().Format("15:04"))
	}

	// -P1W: one week before, should be 14:00 EST on March 1 (still EST)
	a3 := model.Alarm{TriggerValue: "-P1W", Related: "START"}
	got3, err := computeTriggerTime(evt, a3)
	if err != nil {
		t.Fatal(err)
	}
	want3 := time.Date(2026, 3, 1, 14, 0, 0, 0, nyc) // 14:00 EST = 19:00 UTC
	if !got3.Equal(want3) {
		t.Errorf("DST -P1W: got %v (%s UTC), want %v (%s UTC)",
			got3, got3.UTC().Format("15:04"), want3, want3.UTC().Format("15:04"))
	}
}

func TestComputeSnooze_RejectsNegativeDuration(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Negative Dur",
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

	due, _ := svc.Check(ctx, time.Now())
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)

	// Negative duration
	_, err = svc.ComputeSnooze(ctx, pending[0].ID, -5*time.Minute)
	if err == nil {
		t.Error("expected error for negative duration")
	}

	// Zero duration
	_, err = svc.ComputeSnooze(ctx, pending[0].ID, 0)
	if err == nil {
		t.Error("expected error for zero duration")
	}
}

func TestComputeSnooze_RejectsPastEndedEvent(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event started 2 hours ago, ended 1 hour ago
	start := time.Now().Add(-2 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Past Ended",
		StartTime:  start,
		EndTime:    start.Add(time.Hour), // ended 1 hour ago
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

	// Trigger was 2h15m ago, still within 24h stale threshold
	due, _ := svc.Check(ctx, time.Now())
	if len(due) != 1 {
		t.Fatalf("got %d due, want 1", len(due))
	}
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)

	_, err = svc.ComputeSnooze(ctx, pending[0].ID, 10*time.Minute)
	if err == nil {
		t.Error("expected error for past-ended event")
	}
}

func TestComputeSnooze_RejectsDismissedAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	start := time.Now().Add(10 * time.Minute)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Dismissed Alarm",
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

	// Fire, dismiss, then try to snooze
	due, _ := svc.Check(ctx, time.Now())
	if err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)
	if err := svc.Dismiss(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}

	_, err = svc.ComputeSnooze(ctx, pending[0].ID, 10*time.Minute)
	if err == nil {
		t.Error("expected error when snoozing dismissed alarm")
	}
}

func TestComputeSnooze_RejectsNonexistentStateID(t *testing.T) {
	svc, _ := newTestServices(t)
	ctx := context.Background()

	_, err := svc.ComputeSnooze(ctx, 99999, 10*time.Minute)
	if err == nil {
		t.Error("expected error for nonexistent state ID")
	}
	want := "not found"
	if !contains(err.Error(), want) {
		t.Errorf("error %q should contain %q", err.Error(), want)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}
