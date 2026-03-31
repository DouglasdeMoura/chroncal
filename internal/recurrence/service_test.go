package recurrence

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestExpandDailyEvent(t *testing.T) {
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	evt := event.Event{
		ID:             1,
		UID:            "test-daily",
		Title:          "Daily Standup",
		StartTime:      base,
		EndTime:        base.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}

	instances := ExpandEvent(evt, base.Add(-time.Hour), base.AddDate(0, 0, 10))

	if len(instances) != 5 {
		t.Errorf("ExpandEvent() = %d instances, want 5", len(instances))
	}

	for i, inst := range instances {
		want := base.AddDate(0, 0, i)
		if !inst.InstanceTime.Equal(want) {
			t.Errorf("instance[%d] = %v, want %v", i, inst.InstanceTime, want)
		}
	}
}

func TestExpandWeeklyEvent(t *testing.T) {
	base := time.Date(2026, 4, 6, 14, 0, 0, 0, time.UTC) // Monday
	evt := event.Event{
		ID:             2,
		UID:            "test-weekly",
		Title:          "Weekly Review",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=6",
	}

	instances := ExpandEvent(evt, base.Add(-time.Hour), base.AddDate(0, 2, 0))

	if len(instances) != 6 {
		t.Errorf("ExpandEvent() = %d instances, want 6", len(instances))
	}

	// Should be: Mon Apr 6, Wed Apr 8, Fri Apr 10, Mon Apr 13, Wed Apr 15, Fri Apr 17
	expected := []time.Time{
		time.Date(2026, 4, 6, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 17, 14, 0, 0, 0, time.UTC),
	}

	for i, inst := range instances {
		if !inst.InstanceTime.Equal(expected[i]) {
			t.Errorf("instance[%d] = %v, want %v", i, inst.InstanceTime, expected[i])
		}
	}
}

func TestExpandWithExdate(t *testing.T) {
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	evt := event.Event{
		ID:             3,
		UID:            "test-exdate",
		Title:          "Meeting",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
		ExDates:        base.AddDate(0, 0, 2).Format(time.RFC3339), // Exclude day 3
	}

	instances := ExpandEvent(evt, base.Add(-time.Hour), base.AddDate(0, 0, 10))

	if len(instances) != 4 {
		t.Errorf("ExpandEvent() with EXDATE = %d instances, want 4", len(instances))
	}

	// Day 3 (Apr 3) should be excluded
	for _, inst := range instances {
		if inst.InstanceTime.Equal(base.AddDate(0, 0, 2)) {
			t.Error("Found excluded date in instances")
		}
	}
}

func TestExpandWithRDate(t *testing.T) {
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	extraDate := base.AddDate(0, 0, 10)
	evt := event.Event{
		ID:             4,
		UID:            "test-rdate",
		Title:          "Special Event",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
		RDates:         extraDate.Format(time.RFC3339), // Add extra occurrence
	}

	instances := ExpandEvent(evt, base.Add(-time.Hour), extraDate.Add(time.Hour))

	if len(instances) != 4 {
		t.Errorf("ExpandEvent() with RDATE = %d instances, want 4", len(instances))
	}

	// Check that the RDATE is included and marked as override
	foundRDate := false
	for _, inst := range instances {
		if inst.InstanceTime.Equal(extraDate) && inst.IsOverride {
			foundRDate = true
			break
		}
	}
	if !foundRDate {
		t.Error("RDATE instance not found or not marked as override")
	}
}

func TestExpandNonRecurring(t *testing.T) {
	evt := event.Event{
		ID:        5,
		UID:       "test-non-recurring",
		Title:     "One-time Event",
		StartTime: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		// No RecurrenceRule
	}

	instances := ExpandEvent(evt, evt.StartTime.Add(-time.Hour), evt.StartTime.Add(time.Hour))

	if len(instances) != 1 {
		t.Errorf("ExpandEvent() non-recurring = %d instances, want 1", len(instances))
	}

	if !instances[0].InstanceTime.Equal(evt.StartTime) {
		t.Errorf("instance time = %v, want %v", instances[0].InstanceTime, evt.StartTime)
	}
}
