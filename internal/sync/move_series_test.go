package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// syncPairClean syncs B and then A twice and fails on any error or
// conflict.
func syncPairClean(t *testing.T, engine *Engine, calA, calB int64) {
	t.Helper()
	for pass := range 2 {
		for _, calID := range []int64{calB, calA} {
			res, err := engine.SyncCalendar(context.Background(), calID, ConflictPrompt)
			if err != nil {
				t.Fatalf("pass %d SyncCalendar(%d): %v", pass, calID, err)
			}
			if len(res.Errors) != 0 || res.Conflicts != 0 {
				t.Fatalf("pass %d SyncCalendar(%d) = errors %v, conflicts %d; want a clean pass", pass, calID, res.Errors, res.Conflicts)
			}
		}
	}
}

func wantOnlyServerObject(t *testing.T, srv *icloudFakeServer, path string) {
	t.Helper()
	if got := srv.paths(); len(got) != 1 || got[0] != path {
		t.Fatalf("server objects = %v, want only %s", got, path)
	}
}

func countRows(t *testing.T, engine *Engine, table string, calID int64, uid string) int {
	t.Helper()
	var n int
	if err := engine.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM `+table+` WHERE calendar_id = ? AND uid = ?`, calID, uid,
	).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// createSeries creates a daily series of five occurrences on calID.
func createSeries(t *testing.T, engine *Engine, calID int64) event.Event {
	t.Helper()
	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	master, err := engine.events.Create(context.Background(), event.CreateParams{
		CalendarID:     calID,
		Title:          "Standup",
		StartTime:      start,
		EndTime:        start.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return master
}

// TestMoveRecurringSeriesMovesOverrides moves the master of a series with
// one override and one deleted instance. Every row of the UID must move
// with the master, so the source calendar keeps nothing and the server
// object moves as one unit (issue #828).
func TestMoveRecurringSeriesMovesOverrides(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	srv := newICloudFakeServer(t)
	calA, calB := seedICloudPair(t, engine, q, srv)

	master := createSeries(t, engine, calA)
	inst := master.StartTime.Add(24 * time.Hour)
	if _, err := engine.events.UpdateInstance(ctx, master.UID, inst, event.UpdateParams{
		CalendarID: calA,
		Title:      "Standup (late)",
		StartTime:  inst.Add(time.Hour),
		EndTime:    inst.Add(90 * time.Minute),
	}); err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}
	if err := engine.events.DeleteInstance(ctx, master.UID, master.StartTime.Add(48*time.Hour)); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 {
		t.Fatalf("initial push = %+v, %v", res, err)
	}

	fresh, err := engine.events.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, fresh, calB)

	for _, table := range []string{"events", "event_exdate_deletes"} {
		if n := countRows(t, engine, table, calA, master.UID); n != 0 {
			t.Fatalf("%s rows left in source calendar = %d, want 0", table, n)
		}
	}
	if n := countRows(t, engine, "events", calB, master.UID); n != 2 {
		t.Fatalf("event rows in target calendar = %d, want 2 (master and override)", n)
	}

	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/b/"+master.UID+".ics")
	srv.mu.Lock()
	body := srv.objects["/cal/b/"+master.UID+".ics"]
	srv.mu.Unlock()
	if !strings.Contains(body, "RECURRENCE-ID") {
		t.Fatalf("server body has no override:\n%s", body)
	}
}

// TestMoveOverrideRowMovesWholeSeries moves an override row by its ID. The
// master and the other rows of the UID must move with it.
func TestMoveOverrideRowMovesWholeSeries(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	srv := newICloudFakeServer(t)
	calA, calB := seedICloudPair(t, engine, q, srv)

	master := createSeries(t, engine, calA)
	inst := master.StartTime.Add(24 * time.Hour)
	override, err := engine.events.UpdateInstance(ctx, master.UID, inst, event.UpdateParams{
		CalendarID: calA, Title: "Late", StartTime: inst, EndTime: inst.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}
	moveEvent(t, engine, override, calB)
	if n := countRows(t, engine, "events", calA, master.UID); n != 0 {
		t.Fatalf("event rows left in source calendar = %d, want 0", n)
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/b/"+master.UID+".ics")
}

// TestMoveSingleInstanceToOtherCalendarIsRejected proves that one
// occurrence cannot move alone. A CalDAV object holds the whole series, so
// a lone override in another calendar splits the series.
func TestMoveSingleInstanceToOtherCalendarIsRejected(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	srv := newICloudFakeServer(t)
	calA, calB := seedICloudPair(t, engine, q, srv)

	master := createSeries(t, engine, calA)
	inst := master.StartTime.Add(24 * time.Hour)
	_, err := engine.events.UpdateInstance(ctx, master.UID, inst, event.UpdateParams{
		CalendarID: calB, Title: "Elsewhere", StartTime: inst, EndTime: inst.Add(time.Hour),
	})
	if !errors.Is(err, event.ErrInstanceCalendarMove) {
		t.Fatalf("UpdateInstance to another calendar = %v, want ErrInstanceCalendarMove", err)
	}
	if n := countRows(t, engine, "events", calB, master.UID); n != 0 {
		t.Fatalf("event rows in target calendar = %d, want 0", n)
	}
}

// TestMoveTodoBetweenCalendarsOfOneICloudAccount is the todo form of
// issue #828.
func TestMoveTodoBetweenCalendarsOfOneICloudAccount(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	srv := newICloudFakeServer(t)
	calA, calB := seedICloudPair(t, engine, q, srv)

	td, err := engine.todos.Create(ctx, todo.CreateParams{CalendarID: calA, Summary: "Buy milk"})
	if err != nil {
		t.Fatalf("Create todo: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("initial push = %+v, %v", res, err)
	}
	if _, err := engine.todos.Update(ctx, td.ID, todo.UpdateParams{
		CalendarID: calB, Summary: td.Summary, Status: td.Status, Class: td.Class,
	}); err != nil {
		t.Fatalf("Update todo (move): %v", err)
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/b/"+td.UID+".ics")
}

// TestMoveJournalBetweenCalendarsOfOneICloudAccount is the journal form of
// issue #828.
func TestMoveJournalBetweenCalendarsOfOneICloudAccount(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	srv := newICloudFakeServer(t)
	calA, calB := seedICloudPair(t, engine, q, srv)

	j, err := engine.journals.Create(ctx, journal.CreateParams{CalendarID: calA, Summary: "Notes", StartDate: "2026-05-18"})
	if err != nil {
		t.Fatalf("Create journal: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("initial push = %+v, %v", res, err)
	}
	if _, err := engine.journals.Update(ctx, j.ID, journal.UpdateParams{
		CalendarID: calB, Summary: j.Summary, StartDate: "2026-05-18", Status: j.Status, Class: j.Class,
	}); err != nil {
		t.Fatalf("Update journal (move): %v", err)
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/b/"+j.UID+".ics")
}

// TestEditLegacyOverrideInOtherCalendarKeepsCalendar proves that an edit
// of an override that an older version moved to another calendar still
// saves. The edit form preselects the calendar of the override, so the
// check must not refuse that calendar.
func TestEditLegacyOverrideInOtherCalendarKeepsCalendar(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	srv := newICloudFakeServer(t)
	calA, calB := seedICloudPair(t, engine, q, srv)

	master := createSeries(t, engine, calA)
	inst := master.StartTime.Add(24 * time.Hour)
	override, err := engine.events.UpdateInstance(ctx, master.UID, inst, event.UpdateParams{
		CalendarID: calA, Title: "Late", StartTime: inst, EndTime: inst.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE events SET calendar_id = ? WHERE id = ?`, calB, override.ID); err != nil {
		t.Fatalf("move override row: %v", err)
	}

	if _, err := engine.events.UpdateInstance(ctx, master.UID, inst, event.UpdateParams{
		CalendarID: calB, Title: "Later", StartTime: inst, EndTime: inst.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpdateInstance on the legacy override = %v, want nil", err)
	}
}
