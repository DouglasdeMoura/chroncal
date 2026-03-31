package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

func TestCheckRecurringEventAlarm(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)

	// Create a daily recurring event with a 15-min before alarm
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	evt, err := eventsSvc.Create(context.Background(), event.CreateParams{
		CalendarID:     1,
		Title:          "Daily Meeting",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	err = eventsSvc.ReplaceAlarms(context.Background(), evt.ID, []model.Alarm{
		{UID: "alarm-daily-1", Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Daily meeting in 15 minutes"},
	})
	if err != nil {
		t.Fatalf("add alarm: %v", err)
	}

	// Check at 8:50 AM on day 2 (alarm should fire at 8:45 AM)
	checkTime := time.Date(2026, 4, 2, 8, 50, 0, 0, time.UTC)
	svc := NewService(db, q, eventsSvc, nil)
	eventAlarms, _, err := svc.Check(context.Background(), checkTime)
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	// Should find the alarm for the second occurrence (Apr 2, 8:45 AM)
	found := false
	for _, d := range eventAlarms {
		if d.Event.ID == evt.ID {
			triggerAt := time.Date(2026, 4, 2, 8, 45, 0, 0, time.UTC)
			if d.TriggerAt.Equal(triggerAt) {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("Expected to find alarm for recurring event instance on day 2")
	}
}

func TestCheckRecurringEventAlarm_AllInstances(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)

	// Create weekly event with 4 occurrences
	base := time.Date(2026, 4, 6, 14, 0, 0, 0, time.UTC) // Monday
	evt, err := eventsSvc.Create(context.Background(), event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      base,
		EndTime:        base.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO;COUNT=4",
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	err = eventsSvc.ReplaceAlarms(context.Background(), evt.ID, []model.Alarm{
		{UID: "weekly-alarm", Action: "DISPLAY", TriggerValue: "-PT10M", Description: "Weekly sync starting soon"},
	})
	if err != nil {
		t.Fatalf("add alarm: %v", err)
	}

	// Check after the 2nd occurrence to catch 2 alarms
	// 1st alarm: Apr 6 13:50, 2nd alarm: Apr 13 13:50
	// Check on Apr 13 after 13:50 to catch both alarms (2nd is not stale yet)
	checkTime := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	svc := NewService(db, q, eventsSvc, nil)
	eventAlarms, _, err := svc.Check(context.Background(), checkTime)
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	// Should find 2 alarms (1st might be stale depending on timing, but 2nd should be there)
	// Actually, the 1st alarm (Apr 6) would be stale by Apr 13 (> 24h), so only 2nd should be found
	foundCount := 0
	for _, d := range eventAlarms {
		if d.Event.ID == evt.ID {
			foundCount++
		}
	}

	if foundCount != 1 {
		t.Errorf("Found %d alarms for recurring event at 2nd occurrence, want 1 (2nd alarm only, 1st is stale)", foundCount)
	}
}
