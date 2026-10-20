package event

import (
	"context"
	"testing"
	"time"
)

// TestUpdateScopes_CalendarZeroKeepsTheCalendar checks the rule of
// UpdateParams: CalendarID 0 keeps the calendar. The update by row ID
// follows it (uidmove.Rows.Update). The occurrence update and the split
// must follow it too.
func TestUpdateScopes_CalendarZeroKeepsTheCalendar(t *testing.T) {
	ctx := context.Background()
	instance := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)
	p := UpdateParams{
		Title:     "Standup (edit)",
		StartTime: time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
	}

	t.Run("this event", func(t *testing.T) {
		svc := newTestService(t)
		master := newRecurringMaster(t, svc, "zero-cal-1", "FREQ=DAILY")
		got, err := svc.UpdateInstance(ctx, master.UID, instance, p)
		if err != nil {
			t.Fatalf("UpdateInstance with CalendarID 0: %v", err)
		}
		if got.CalendarID != master.CalendarID {
			t.Fatalf("override calendar = %d, want %d", got.CalendarID, master.CalendarID)
		}
		// A second edit updates the stored override.
		if got, err = svc.UpdateInstance(ctx, master.UID, instance, p); err != nil {
			t.Fatalf("second UpdateInstance with CalendarID 0: %v", err)
		} else if got.CalendarID != master.CalendarID {
			t.Fatalf("updated override calendar = %d, want %d", got.CalendarID, master.CalendarID)
		}
	})

	t.Run("this and following", func(t *testing.T) {
		svc := newTestService(t)
		master := newRecurringMaster(t, svc, "zero-cal-2", "FREQ=DAILY")
		got, err := svc.UpdateFromInstance(ctx, master.CalendarID, master.UID, instance, p)
		if err != nil {
			t.Fatalf("UpdateFromInstance with CalendarID 0: %v", err)
		}
		if got.CalendarID != master.CalendarID {
			t.Fatalf("split series calendar = %d, want %d", got.CalendarID, master.CalendarID)
		}
	})
}
