package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestOverrideCalendarMoveCLIError proves that `event update`, `todo
// update`, and `journal update` refuse the calendar move of one override
// row with a readable error. The error names the command that moves the
// whole series by the numeric ID of the master. A UID can have a master in
// two calendars, so a UID in the hint can name the wrong series.
func TestOverrideCalendarMoveCLIError(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	home, err := a.Calendars.Create(ctx, "Home", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	asOverride := func(table, uid string, id int64) {
		t.Helper()
		if _, err := a.DB.ExecContext(ctx,
			`UPDATE `+table+` SET uid = ?, recurrence_id = '2026-05-19T09:00:00Z' WHERE id = ?`, uid, id,
		); err != nil {
			t.Fatalf("seed override: %v", err)
		}
	}
	tdMaster, err := a.Todos.Create(ctx, todo.CreateParams{CalendarID: 1, Summary: "Water", DueDate: "2026-05-18T09:00:00Z", RecurrenceRule: "FREQ=DAILY;COUNT=3"})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	tdOverride, err := a.Todos.Create(ctx, todo.CreateParams{CalendarID: 1, Summary: "Water late"})
	if err != nil {
		t.Fatalf("create todo override: %v", err)
	}
	asOverride("todos", tdMaster.UID, tdOverride.ID)
	jMaster, err := a.Journals.Create(ctx, journal.CreateParams{CalendarID: 1, Summary: "Log", StartDate: "2026-05-18", RecurrenceRule: "FREQ=DAILY;COUNT=3"})
	if err != nil {
		t.Fatalf("create journal: %v", err)
	}
	jOverride, err := a.Journals.Create(ctx, journal.CreateParams{CalendarID: 1, Summary: "Log late", StartDate: "2026-05-19"})
	if err != nil {
		t.Fatalf("create journal override: %v", err)
	}
	asOverride("journals", jMaster.UID, jOverride.ID)

	// The event series has a copy in Home with the same UID. The hint must
	// name the master in calendar 1, not the UID.
	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	evMaster, err := a.Events.Create(ctx, event.CreateParams{CalendarID: 1, Title: "Standup", StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3"})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	inst := start.Add(24 * time.Hour)
	evOverride, err := a.Events.UpdateInstance(ctx, evMaster.UID, inst, event.UpdateParams{CalendarID: 1, Title: "Late", StartTime: inst, EndTime: inst.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create event override: %v", err)
	}
	evCopy, err := a.Events.Create(ctx, event.CreateParams{CalendarID: home.ID, Title: "Standup copy", StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3"})
	if err != nil {
		t.Fatalf("create event copy: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx, `UPDATE events SET uid = ? WHERE id = ?`, evMaster.UID, evCopy.ID); err != nil {
		t.Fatalf("seed the copy: %v", err)
	}
	a.Close()

	for _, tc := range []struct {
		kind     string
		id       int64
		masterID int64
	}{
		{"event", evOverride.ID, evMaster.ID},
		{"todo", tdOverride.ID, tdMaster.ID},
		{"journal", jOverride.ID, jMaster.ID},
	} {
		_, stderr, err := runChroncalCommand(t, tc.kind, "update", fmt.Sprint(tc.id), "--calendar", home.Name)
		if err == nil {
			t.Fatalf("%s update of an override to another calendar succeeded", tc.kind)
		}
		want := fmt.Sprintf("chroncal %s update %d --calendar NAME", tc.kind, tc.masterID)
		if !strings.Contains(stderr, "one occurrence cannot move to another calendar") || !strings.Contains(stderr, want) {
			t.Fatalf("%s update stderr = %q, want the reason and %q", tc.kind, stderr, want)
		}
	}
}
