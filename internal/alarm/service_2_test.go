package alarm

import (
	"context"

	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"

	"github.com/douglasdemoura/chroncal/internal/todo"
)

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

	due, _, err := svc.Check(ctx, time.Now())
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

	due, _, _ := svc.Check(ctx, time.Now())
	if _, err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)

	// Negative duration
	_, err = svc.ComputeSnooze(ctx, pending[0].ID, -5*time.Minute, time.Now())
	if err == nil {
		t.Error("expected error for negative duration")
	}

	// Zero duration
	_, err = svc.ComputeSnooze(ctx, pending[0].ID, 0, time.Now())
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
	due, _, _ := svc.Check(ctx, time.Now())
	if len(due) != 1 {
		t.Fatalf("got %d due, want 1", len(due))
	}
	if _, err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)

	_, err = svc.ComputeSnooze(ctx, pending[0].ID, 10*time.Minute, time.Now())
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
	due, _, _ := svc.Check(ctx, time.Now())
	if _, err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatal(err)
	}
	pending, _ := svc.ListPending(ctx)
	if err := svc.Dismiss(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}

	_, err = svc.ComputeSnooze(ctx, pending[0].ID, 10*time.Minute, time.Now())
	if err == nil {
		t.Error("expected error when snoozing dismissed alarm")
	}
}

func TestCheckMissed_FindsStaleAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event was 3 days ago with a 15-min-before alarm.
	// Trigger was 3 days + 15 minutes ago, well past the 24h stale threshold.
	start := time.Now().Add(-72 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Missed Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})

	missed, _, err := svc.CheckMissed(ctx, time.Now(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 1 {
		t.Fatalf("got %d missed, want 1", len(missed))
	}
	if missed[0].EventTitle != "Missed Meeting" {
		t.Errorf("title = %q", missed[0].EventTitle)
	}
}

func TestCheckMissed_SkipsAcknowledged(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	start := time.Now().Add(-72 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Acknowledged Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})

	// Fire via Check at the correct time (simulate daemon having run).
	triggerTime := start.Add(-15 * time.Minute)
	due, _, err := svc.Check(ctx, triggerTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		svc.MarkFired(ctx, d) //nolint:errcheck // fire-and-forget in bulk setup
	}

	missed, _, err := svc.CheckMissed(ctx, time.Now(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 0 {
		t.Fatalf("got %d missed, want 0 (acknowledged)", len(missed))
	}
}

func TestCheckMissed_SkipsNotYetStale(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event 12 hours ago, alarm trigger 12h15m ago. Under the 24h threshold.
	start := time.Now().Add(-12 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Recent Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})

	missed, _, err := svc.CheckMissed(ctx, time.Now(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 0 {
		t.Fatalf("got %d missed, want 0 (not yet stale)", len(missed))
	}
}

func TestCheckMissed_FindsStaleTodoAlarm(t *testing.T) {
	svc, _, todoSvc := newTestServicesWithTodos(t)
	ctx := context.Background()

	// Todo due 3 days ago with a 15-min-before alarm.
	dueDate := time.Now().Add(-72 * time.Hour)
	td, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "Missed Task",
		DueDate:    dueDate.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	todoSvc.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})

	_, missedTodos, err := svc.CheckMissed(ctx, time.Now(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(missedTodos) != 1 {
		t.Fatalf("got %d missed todos, want 1", len(missedTodos))
	}
	if missedTodos[0].TodoSummary != "Missed Task" {
		t.Errorf("summary = %q", missedTodos[0].TodoSummary)
	}
}

func TestCheckMissed_SkipsCompletedTodo(t *testing.T) {
	svc, _, todoSvc := newTestServicesWithTodos(t)
	ctx := context.Background()

	dueDate := time.Now().Add(-72 * time.Hour)
	td, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "Done Task",
		DueDate:    dueDate.Format(time.RFC3339),
		Status:     "COMPLETED",
	})
	if err != nil {
		t.Fatal(err)
	}
	todoSvc.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})

	_, missedTodos, err := svc.CheckMissed(ctx, time.Now(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(missedTodos) != 0 {
		t.Fatalf("got %d missed todos, want 0 (completed)", len(missedTodos))
	}
}

func TestCheckMissed_SkipsNotYetStaleTodo(t *testing.T) {
	svc, _, todoSvc := newTestServicesWithTodos(t)
	ctx := context.Background()

	// Todo due 12 hours ago. Trigger 12h15m ago, under 24h stale threshold.
	dueDate := time.Now().Add(-12 * time.Hour)
	td, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "Recent Task",
		DueDate:    dueDate.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	todoSvc.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	})

	_, missedTodos, err := svc.CheckMissed(ctx, time.Now(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(missedTodos) != 0 {
		t.Fatalf("got %d missed todos, want 0 (not yet stale)", len(missedTodos))
	}
}

// TestCheck_FiresLongLeadTimeAlarm guards issue #98: an alarm with a lead time
// beyond the fixed 48h expansion window (e.g. -P1W on an event 7 days out)
// must still fire when its trigger time arrives. The event instance sits far
// past now+48h, so a fixed window silently drops it.
func TestCheck_FiresLongLeadTimeAlarm(t *testing.T) {
	svc, evtSvc := newTestServices(t)
	ctx := context.Background()

	// Event starts in exactly 7 days; a "1 week before" alarm is due now.
	start := time.Now().Add(7 * 24 * time.Hour)
	e, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Far Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-P1W", Description: "1 week reminder"},
	})
	if err != nil {
		t.Fatal(err)
	}

	due, _, err := svc.Check(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due alarms, want 1 (long-lead-time alarm missed)", len(due))
	}
	if due[0].Event.ID != e.ID {
		t.Errorf("event ID = %d, want %d", due[0].Event.ID, e.ID)
	}
}
