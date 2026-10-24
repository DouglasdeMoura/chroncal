package recurrence

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestListExpandedEventsWithAlarms guards issue #912. With WithAlarms each
// row carries the alarms that the event view loads for its ID: a generated
// occurrence has the alarms of the master, an override has its own, and a
// one-off has its own.
func TestListExpandedEventsWithAlarms(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	recur := NewService(db, q)
	ctx := context.Background()

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := events.Create(ctx, event.CreateParams{CalendarID: 1, Title: "Standup",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3"})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	if err := events.ReplaceAlarms(ctx, master.ID, []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT5M", Related: "START"}}); err != nil {
		t.Fatalf("master alarms: %v", err)
	}
	slot := start.AddDate(0, 0, 1)
	override, err := events.UpdateInstance(ctx, 1, master.UID, slot, event.UpdateParams{CalendarID: 1, Title: "Late",
		StartTime: slot.Add(time.Hour), EndTime: slot.Add(2 * time.Hour)})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}
	if err := events.ReplaceAlarms(ctx, override.ID, []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT30M", Related: "START"}}); err != nil {
		t.Fatalf("override alarms: %v", err)
	}
	oneOff, err := events.Create(ctx, event.CreateParams{CalendarID: 1, Title: "Lunch",
		StartTime: start.Add(3 * time.Hour), EndTime: start.Add(4 * time.Hour)})
	if err != nil {
		t.Fatalf("create one-off: %v", err)
	}
	if err := events.ReplaceAlarms(ctx, oneOff.ID, []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT1H", Related: "START"}}); err != nil {
		t.Fatalf("one-off alarms: %v", err)
	}
	bare, err := events.Create(ctx, event.CreateParams{CalendarID: 1, Title: "Bare",
		StartTime: start.Add(5 * time.Hour), EndTime: start.Add(6 * time.Hour)})
	if err != nil {
		t.Fatalf("create bare event: %v", err)
	}

	from, to := start.Add(-time.Hour), start.AddDate(0, 0, 4)
	got, err := recur.ListExpandedEvents(ctx, from, to, WithAlarms())
	if err != nil {
		t.Fatalf("ListExpandedEvents: %v", err)
	}
	want := map[int64]string{master.ID: "-PT5M", override.ID: "-PT30M", oneOff.ID: "-PT1H", bare.ID: ""}
	seen := map[int64]int{}
	for _, x := range got {
		wantTrigger, ok := want[x.ID]
		if !ok {
			t.Fatalf("unexpected row %d %q", x.ID, x.Title)
		}
		seen[x.ID]++
		// The event view loads the alarms of the row ID.
		viewAlarms, err := events.ListAlarms(ctx, x.ID)
		if err != nil {
			t.Fatalf("ListAlarms(%d): %v", x.ID, err)
		}
		if len(x.Alarms) != len(viewAlarms) {
			t.Fatalf("row %d %q at %s: %d alarms, the event view has %d", x.ID, x.Title, x.InstanceTime, len(x.Alarms), len(viewAlarms))
		}
		switch {
		case wantTrigger == "" && len(x.Alarms) != 0:
			t.Fatalf("row %q has alarms %v, want none", x.Title, x.Alarms)
		case wantTrigger != "" && (len(x.Alarms) != 1 || x.Alarms[0].TriggerValue != wantTrigger):
			t.Fatalf("row %q at %s has alarms %v, want %s", x.Title, x.InstanceTime, x.Alarms, wantTrigger)
		}
	}
	if seen[master.ID] != 2 || seen[override.ID] != 1 {
		t.Fatalf("rows per ID = %v, want two generated occurrences and one override", seen)
	}

	plain, err := recur.ListExpandedEvents(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedEvents: %v", err)
	}
	for _, x := range plain {
		if len(x.Alarms) != 0 {
			t.Fatalf("row %q has alarms without WithAlarms", x.Title)
		}
	}
}
