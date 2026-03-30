package recurrence

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/testutil"
)

func TestRecurringEventAlarmFlow(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	eventsSvc := event.NewService(db, q)
	recurSvc := NewService(db, q)

	// Create weekly event with alarm
	base := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC) // Monday
	evt, err := eventsSvc.Create(context.Background(), event.CreateParams{
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      base,
		EndTime:        base.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO;COUNT=4",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Add alarm separately
	err = eventsSvc.ReplaceAlarms(context.Background(), evt.ID, []model.Alarm{{
		UID:          "weekly-alarm",
		Action:       "DISPLAY",
		TriggerValue: "-PT10M",
		Description:  "Weekly sync starting soon",
	}})
	if err != nil {
		t.Fatalf("add alarm: %v", err)
	}

	// Verify expansion works
	instances := ExpandEvent(evt, base.Add(-time.Hour), base.AddDate(0, 1, 0))
	if len(instances) != 4 {
		t.Errorf("instances = %d, want 4", len(instances))
	}

	// Cache instances
	if err := recurSvc.ExpandAndCache(context.Background(), evt,
		base.Add(-time.Hour), base.AddDate(0, 1, 0)); err != nil {
		t.Fatalf("cache: %v", err)
	}

	// Verify cached instances
	count, err := q.CountRecurrenceInstances(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 4 {
		t.Errorf("cached instances = %d, want 4", count)
	}
}
