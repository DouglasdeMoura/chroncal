package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestOverrideCalendarMoveCLIError proves that `todo update` and `journal
// update` refuse the calendar move of one override row with a readable
// error. The error names the command that moves the whole series.
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
	a.Close()

	for _, tc := range []struct {
		kind string
		id   int64
		uid  string
	}{
		{"todo", tdOverride.ID, tdMaster.UID},
		{"journal", jOverride.ID, jMaster.UID},
	} {
		_, stderr, err := runChroncalCommand(t, tc.kind, "update", fmt.Sprint(tc.id), "--calendar", home.Name)
		if err == nil {
			t.Fatalf("%s update of an override to another calendar succeeded", tc.kind)
		}
		want := fmt.Sprintf("chroncal %s update %s --calendar NAME", tc.kind, tc.uid)
		if !strings.Contains(stderr, "one occurrence cannot move to another calendar") || !strings.Contains(stderr, want) {
			t.Fatalf("%s update stderr = %q, want the reason and %q", tc.kind, stderr, want)
		}
	}
}
