package recurrence

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func TestExpandEvent_MultiDayInstanceStraddlingWindowStart(t *testing.T) {
	// A weekly 3-day on-call block: Friday 09:00 -> Sunday 09:00.
	friday := time.Date(2026, 4, 3, 9, 0, 0, 0, time.UTC) // first Friday
	evt := event.Event{
		ID:             1,
		UID:            "oncall-weekly",
		Title:          "On-call",
		StartTime:      friday,
		EndTime:        friday.AddDate(0, 0, 2), // Sunday 09:00 (48h span)
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=FR;COUNT=4",
	}

	// Query a window that opens Saturday: the block began Friday (before the
	// window) but runs through Sunday, so it overlaps and must appear.
	from := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC) // Saturday
	to := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)   // Sunday 00:00

	instances := ExpandEvent(evt, from, to)
	if len(instances) != 1 {
		t.Fatalf("ExpandEvent() = %d instances, want 1 (straddling instance dropped)", len(instances))
	}
	if !instances[0].InstanceTime.Equal(friday) {
		t.Errorf("instance start = %v, want %v", instances[0].InstanceTime, friday)
	}
}

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

func TestExpandEvent_CancelledMasterHasNoOccurrences(t *testing.T) {
	base := time.Date(2026, 4, 6, 14, 0, 0, 0, time.UTC)
	evt := event.Event{
		UID:            "cancelled-weekly",
		Title:          "Cancelled Weekly",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		Status:         "CANCELLED",
	}

	if got := ExpandEvent(evt, base.Add(-time.Hour), base.AddDate(0, 2, 0)); got != nil {
		t.Errorf("cancelled recurring master expanded into %d instances, want none", len(got))
	}

	// A cancelled NON-recurring event is left to the caller (still returned).
	evt.RecurrenceRule = ""
	if got := ExpandEvent(evt, base.Add(-time.Hour), base.Add(time.Hour)); len(got) != 1 {
		t.Errorf("cancelled non-recurring event = %d instances, want 1 (caller decides)", len(got))
	}
}

func TestExpandTodo_CancelledMasterHasNoOccurrences(t *testing.T) {
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	td := todo.Todo{
		UID:            "cancelled-todo",
		Summary:        "Cancelled Weekly Todo",
		DueDate:        "2026-04-06",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		Status:         "CANCELLED",
	}
	if got := ExpandTodo(td, from, to); got != nil {
		t.Errorf("cancelled recurring todo master expanded into %d instances, want none", len(got))
	}
	// A cancelled non-recurring todo is still returned (caller decides).
	td.RecurrenceRule = ""
	if got := ExpandTodo(td, from, to); len(got) != 1 {
		t.Errorf("cancelled non-recurring todo = %d instances, want 1", len(got))
	}
}

func TestExpandJournal_CancelledMasterHasNoOccurrences(t *testing.T) {
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	j := journal.Journal{
		UID:            "cancelled-journal",
		StartDate:      "2026-04-06",
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		Status:         "CANCELLED",
	}
	if got := ExpandJournal(j, from, to); got != nil {
		t.Errorf("cancelled recurring journal master expanded into %d instances, want none", len(got))
	}
	// A cancelled non-recurring journal is still returned (caller decides).
	j.RecurrenceRule = ""
	if got := ExpandJournal(j, from, to); len(got) != 1 {
		t.Errorf("cancelled non-recurring journal = %d instances, want 1", len(got))
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

func TestExpandWithSubSecondRDate(t *testing.T) {
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	// RDATE carrying fractional seconds. The rrule iterator yields RDATE
	// values truncated to whole seconds, so a raw time.Time map lookup
	// (which compares the monotonic-free wall clock incl. nanoseconds)
	// misses and the occurrence is mislabelled as a normal RRULE instance.
	extraDate := base.AddDate(0, 0, 10).Add(500 * time.Millisecond)
	evt := event.Event{
		ID:             40,
		UID:            "test-rdate-subsecond",
		Title:          "Special Event",
		StartTime:      base,
		EndTime:        base.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
		RDates:         extraDate.Format(time.RFC3339Nano),
	}

	instances := ExpandEvent(evt, base.Add(-time.Hour), extraDate.Add(time.Hour))

	// The added occurrence (truncated to the second) must be marked override.
	want := extraDate.Truncate(time.Second)
	found := false
	for _, inst := range instances {
		if inst.InstanceTime.Equal(want) {
			found = true
			if !inst.IsOverride {
				t.Errorf("sub-second RDATE instance at %s not marked as override", want)
			}
		}
	}
	if !found {
		t.Fatalf("RDATE instance at %s not found among %d instances", want, len(instances))
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

func TestExpandEvent_DSTSpringForward(t *testing.T) {
	// In America/New_York, DST spring forward happens on 2026-03-08 at 2:00 AM.
	// A weekly 9am event should stay at 9am local on both sides.
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York timezone not available")
	}

	// DTSTART is before DST: 9am EST = 14:00 UTC
	dtstart := time.Date(2026, 3, 1, 14, 0, 0, 0, time.UTC) // Sun Mar 1, 9am EST
	evt := event.Event{
		UID:            "dst-spring",
		Title:          "Weekly 9am",
		StartTime:      dtstart,
		EndTime:        dtstart.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=4",
		Timezone:       "America/New_York",
	}

	from := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	instances := ExpandEvent(evt, from, to)

	if len(instances) != 4 {
		t.Fatalf("got %d instances, want 4", len(instances))
	}

	for i, inst := range instances {
		local := inst.InstanceTime.In(nyc)
		if local.Hour() != 9 {
			t.Errorf("instance[%d] at %v: local hour = %d, want 9", i, inst.InstanceTime, local.Hour())
		}
	}
}

func TestExpandEvent_DSTFallBack(t *testing.T) {
	// In America/New_York, DST fall back happens on 2025-11-02 at 2:00 AM.
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York timezone not available")
	}

	// DTSTART is before fall-back: 9am EDT = 13:00 UTC
	dtstart := time.Date(2025, 10, 26, 13, 0, 0, 0, time.UTC) // Sun Oct 26, 9am EDT
	evt := event.Event{
		UID:            "dst-fall",
		Title:          "Weekly 9am",
		StartTime:      dtstart,
		EndTime:        dtstart.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=4",
		Timezone:       "America/New_York",
	}

	from := time.Date(2025, 10, 25, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	instances := ExpandEvent(evt, from, to)

	if len(instances) != 4 {
		t.Fatalf("got %d instances, want 4", len(instances))
	}

	for i, inst := range instances {
		local := inst.InstanceTime.In(nyc)
		if local.Hour() != 9 {
			t.Errorf("instance[%d] at %v: local hour = %d, want 9", i, inst.InstanceTime, local.Hour())
		}
	}
}

func TestExpandEvent_FloatingTime(t *testing.T) {
	// No timezone set: expansion should pass through in UTC unchanged.
	dtstart := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	evt := event.Event{
		UID:            "floating",
		Title:          "Floating 9am",
		StartTime:      dtstart,
		EndTime:        dtstart.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=3",
		Timezone:       "", // no timezone
	}

	from := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	instances := ExpandEvent(evt, from, to)

	if len(instances) != 3 {
		t.Fatalf("got %d instances, want 3", len(instances))
	}

	for i, inst := range instances {
		if inst.InstanceTime.Hour() != 9 {
			t.Errorf("instance[%d] hour = %d, want 9", i, inst.InstanceTime.Hour())
		}
		if inst.InstanceTime.Location() != time.UTC {
			t.Errorf("instance[%d] location = %v, want UTC", i, inst.InstanceTime.Location())
		}
	}
}

// The following tests lock in recurring-expansion behavior for the todo and
// journal paths (events are exercised above), so a shared expansion core can
// be proven behavior-preserving for all three entity kinds.

func TestExpandTodo_DailyRecurring(t *testing.T) {
	td := todo.Todo{
		UID:            "daily-todo",
		Summary:        "Daily Task",
		DueDate:        "2026-04-01T09:00:00Z",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	instances := ExpandTodo(td, from, to)
	if len(instances) != 5 {
		t.Fatalf("ExpandTodo() = %d instances, want 5", len(instances))
	}
	for i, inst := range instances {
		want := time.Date(2026, 4, 1+i, 9, 0, 0, 0, time.UTC)
		if !inst.InstanceTime.Equal(want) {
			t.Errorf("instance[%d] = %v, want %v", i, inst.InstanceTime, want)
		}
	}
}

func TestExpandTodo_WithExDateAndRDate(t *testing.T) {
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	extra := base.AddDate(0, 0, 10)
	td := todo.Todo{
		UID:            "todo-exrd",
		Summary:        "Task",
		DueDate:        base.Format(time.RFC3339),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
		ExDates:        base.AddDate(0, 0, 2).Format(time.RFC3339), // drop day 3
		RDates:         extra.Format(time.RFC3339),                 // add extra
	}

	instances := ExpandTodo(td, base.Add(-time.Hour), extra.Add(time.Hour))
	// 5 from RRULE - 1 EXDATE + 1 RDATE = 5.
	if len(instances) != 5 {
		t.Fatalf("ExpandTodo() = %d instances, want 5", len(instances))
	}
	foundRDate := false
	for _, inst := range instances {
		if inst.InstanceTime.Equal(base.AddDate(0, 0, 2)) {
			t.Error("excluded EXDATE present in instances")
		}
		if inst.InstanceTime.Equal(extra) && inst.IsOverride {
			foundRDate = true
		}
	}
	if !foundRDate {
		t.Error("RDATE instance not found or not marked as override")
	}
}

func TestExpandJournal_DailyRecurring(t *testing.T) {
	j := journal.Journal{
		UID:            "daily-journal",
		StartDate:      "2026-04-01",
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	instances := ExpandJournal(j, from, to)
	if len(instances) != 5 {
		t.Fatalf("ExpandJournal() = %d instances, want 5", len(instances))
	}
	for i, inst := range instances {
		want := time.Date(2026, 4, 1+i, 0, 0, 0, 0, time.UTC)
		if !inst.InstanceTime.Equal(want) {
			t.Errorf("instance[%d] = %v, want %v", i, inst.InstanceTime, want)
		}
	}
}

func TestExpandJournal_WithExDateAndRDate(t *testing.T) {
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	extra := base.AddDate(0, 0, 10)
	j := journal.Journal{
		UID:            "journal-exrd",
		StartDate:      base.Format(time.RFC3339),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
		ExDates:        base.AddDate(0, 0, 2).Format(time.RFC3339),
		RDates:         extra.Format(time.RFC3339),
	}

	instances := ExpandJournal(j, base.Add(-time.Hour), extra.Add(time.Hour))
	if len(instances) != 5 {
		t.Fatalf("ExpandJournal() = %d instances, want 5", len(instances))
	}
	foundRDate := false
	for _, inst := range instances {
		if inst.InstanceTime.Equal(base.AddDate(0, 0, 2)) {
			t.Error("excluded EXDATE present in instances")
		}
		if inst.InstanceTime.Equal(extra) && inst.IsOverride {
			foundRDate = true
		}
	}
	if !foundRDate {
		t.Error("RDATE instance not found or not marked as override")
	}
}

// TestExpandEvent_RDateOnlyNoRRule locks in RFC 5545 §3.8.5.2: an event with
// RDATEs but no RRULE must expand to its DTSTART occurrence and all
// explicitly-listed RDATE occurrences (issue #362). Previously newRRuleSet
// returned ok=false whenever rule == "", causing the rdateSet to be silently
// ignored and only the DTSTART occurrence to appear.
func TestExpandEvent_RDateOnlyNoRRule(t *testing.T) {
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)

	evt := event.Event{
		ID:        100,
		UID:       "rdate-only-event",
		Title:     "Irregular Meeting",
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		// No RecurrenceRule — pure RDATE recurrence.
		RDates: rdate1.Format(time.RFC3339) + "," + rdate2.Format(time.RFC3339),
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	instances := ExpandEvent(evt, from, to)
	// Expect: DTSTART (Apr 1) + RDATE1 (Apr 15) + RDATE2 (Apr 22) = 3 instances.
	if len(instances) != 3 {
		t.Fatalf("ExpandEvent() = %d instances, want 3 (DTSTART + 2 RDATEs)", len(instances))
	}

	wantTimes := []time.Time{base, rdate1, rdate2}
	for i, want := range wantTimes {
		if !instances[i].InstanceTime.Equal(want) {
			t.Errorf("instance[%d] = %v, want %v", i, instances[i].InstanceTime, want)
		}
	}

	// DTSTART occurrence must NOT be marked IsOverride (it is the canonical start).
	if instances[0].IsOverride {
		t.Error("DTSTART occurrence must not be marked IsOverride")
	}
	// RDATE occurrences must be marked IsOverride.
	if !instances[1].IsOverride {
		t.Errorf("RDATE occurrence at %v not marked IsOverride", instances[1].InstanceTime)
	}
	if !instances[2].IsOverride {
		t.Errorf("RDATE occurrence at %v not marked IsOverride", instances[2].InstanceTime)
	}
}

// TestExpandEvent_RDateOnlyWithExDate verifies that an EXDATE on the DTSTART
// of an RDATE-only event suppresses that occurrence while leaving the RDATEs.
func TestExpandEvent_RDateOnlyWithExDate(t *testing.T) {
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)

	evt := event.Event{
		ID:        101,
		UID:       "rdate-only-exdate",
		Title:     "Irregular Meeting (exdated start)",
		StartTime: base,
		EndTime:   base.Add(time.Hour),
		ExDates:   base.Format(time.RFC3339), // exclude DTSTART
		RDates:    rdate1.Format(time.RFC3339),
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	instances := ExpandEvent(evt, from, to)
	// DTSTART is excluded; only the RDATE should appear.
	if len(instances) != 1 {
		t.Fatalf("ExpandEvent() = %d instances, want 1 (DTSTART excluded by EXDATE)", len(instances))
	}
	if !instances[0].InstanceTime.Equal(rdate1) {
		t.Errorf("instance = %v, want %v", instances[0].InstanceTime, rdate1)
	}
}

// TestExpandTodo_RDateOnlyNoRRule mirrors TestExpandEvent_RDateOnlyNoRRule for
// the todo entity.
func TestExpandTodo_RDateOnlyNoRRule(t *testing.T) {
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)

	td := todo.Todo{
		UID:     "rdate-only-todo",
		Summary: "Irregular Task",
		DueDate: base.Format(time.RFC3339),
		// No RecurrenceRule.
		RDates: rdate1.Format(time.RFC3339),
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	instances := ExpandTodo(td, from, to)
	if len(instances) != 2 {
		t.Fatalf("ExpandTodo() = %d instances, want 2 (DTSTART + 1 RDATE)", len(instances))
	}
	if !instances[0].InstanceTime.Equal(base) {
		t.Errorf("instance[0] = %v, want %v (DTSTART)", instances[0].InstanceTime, base)
	}
	if !instances[1].InstanceTime.Equal(rdate1) {
		t.Errorf("instance[1] = %v, want %v (RDATE)", instances[1].InstanceTime, rdate1)
	}
}

// TestExpandJournal_RDateOnlyNoRRule mirrors TestExpandEvent_RDateOnlyNoRRule
// for the journal entity.
func TestExpandJournal_RDateOnlyNoRRule(t *testing.T) {
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	rdate1 := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	j := journal.Journal{
		UID:       "rdate-only-journal",
		StartDate: base.Format(time.RFC3339),
		// No RecurrenceRule.
		RDates: rdate1.Format(time.RFC3339),
	}

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	instances := ExpandJournal(j, from, to)
	if len(instances) != 2 {
		t.Fatalf("ExpandJournal() = %d instances, want 2 (DTSTART + 1 RDATE)", len(instances))
	}
	if !instances[0].InstanceTime.Equal(base) {
		t.Errorf("instance[0] = %v, want %v (DTSTART)", instances[0].InstanceTime, base)
	}
	if !instances[1].InstanceTime.Equal(rdate1) {
		t.Errorf("instance[1] = %v, want %v (RDATE)", instances[1].InstanceTime, rdate1)
	}
}

// TestExpandJournal_HalfOpenWindow locks in that journal expansion honors the
// half-open [from, to) window: an occurrence exactly at from is kept, one
// exactly at to is excluded.
func TestExpandJournal_HalfOpenWindow(t *testing.T) {
	j := journal.Journal{
		UID:            "journal-window",
		StartDate:      "2026-04-01",
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	}
	from := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)

	instances := ExpandJournal(j, from, to)
	// Apr 3, 4, 5 included; Apr 6 (== to) excluded.
	if len(instances) != 3 {
		t.Fatalf("ExpandJournal() = %d instances, want 3", len(instances))
	}
	if !instances[0].InstanceTime.Equal(from) {
		t.Errorf("first instance = %v, want %v (from boundary inclusive)", instances[0].InstanceTime, from)
	}
	for _, inst := range instances {
		if !inst.InstanceTime.Before(to) {
			t.Errorf("instance %v not before to boundary %v", inst.InstanceTime, to)
		}
	}
}
