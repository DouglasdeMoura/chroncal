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

// createSeriesWithOverride creates a daily series on calID and one
// override of the second occurrence. inst is the start of that occurrence.
func createSeriesWithOverride(t *testing.T, engine *Engine, calID int64) (master, override event.Event, inst time.Time) {
	t.Helper()
	master = createSeries(t, engine, calID)
	inst = master.StartTime.Add(24 * time.Hour)
	override, err := engine.events.UpdateInstance(context.Background(), master.UID, inst, event.UpdateParams{
		CalendarID: calID, Title: "Late", StartTime: inst, EndTime: inst.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}
	return master, override, inst
}

// TestMoveRecurringSeriesMovesOverrides moves the master of a series with
// one override and one deleted instance. Every row of the UID must move
// with the master, so the source calendar keeps nothing and the server
// object moves as one unit (issue #828).
func TestMoveRecurringSeriesMovesOverrides(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	master, _, _ := createSeriesWithOverride(t, engine, calA)
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

// TestMoveOverrideRowIsRejected updates an override row by its ID with
// another calendar. UpdateInstance refuses that request, so Update must
// refuse it too and change nothing. An edit that keeps the calendar still
// saves.
func TestMoveOverrideRowIsRejected(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	master, override, _ := createSeriesWithOverride(t, engine, calA)
	params := event.UpdateParams{
		Title:      override.Title,
		StartTime:  override.StartTime,
		EndTime:    override.EndTime,
		CalendarID: calB,
		Status:     override.Status,
		Transp:     override.Transp,
		Class:      override.Class,
	}
	if _, err := engine.events.Update(ctx, override.ID, params); !errors.Is(err, event.ErrInstanceCalendarMove) {
		t.Fatalf("Update of an override to another calendar = %v, want ErrInstanceCalendarMove", err)
	}
	if _, err := engine.events.UpdateWithRelations(ctx, override.ID, params, nil, nil); !errors.Is(err, event.ErrInstanceCalendarMove) {
		t.Fatalf("UpdateWithRelations of an override to another calendar = %v, want ErrInstanceCalendarMove", err)
	}
	if n := countRows(t, engine, "events", calA, master.UID); n != 2 {
		t.Fatalf("event rows in calendar A = %d, want 2 (master and override)", n)
	}
	if n := countRows(t, engine, "events", calB, master.UID); n != 0 {
		t.Fatalf("event rows in calendar B = %d, want 0", n)
	}

	params.CalendarID = calA
	params.Title = "Renamed"
	if _, err := engine.events.Update(ctx, override.ID, params); err != nil {
		t.Fatalf("Update of an override in its own calendar = %v, want nil", err)
	}
}

// TestUpdateLegacyOverrideRowBackToMasterCalendar updates an override row
// that an older version saved in calendar B. The master stays in A. Update
// permits the move back to A, as UpdateInstance does, and the whole series
// is then in A.
func TestUpdateLegacyOverrideRowBackToMasterCalendar(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	master, override, _ := createSeriesWithOverride(t, engine, calA)
	if _, err := engine.db.ExecContext(ctx, `UPDATE events SET calendar_id = ? WHERE id = ?`, calB, override.ID); err != nil {
		t.Fatalf("move override row: %v", err)
	}
	moveEvent(t, engine, override, calA)
	if n := countRows(t, engine, "events", calB, master.UID); n != 0 {
		t.Fatalf("event rows left in calendar B = %d, want 0", n)
	}
}

// TestMoveSingleInstanceToOtherCalendarIsRejected proves that one
// occurrence cannot move alone. A CalDAV object holds the whole series, so
// a lone override in another calendar splits the series.
func TestMoveSingleInstanceToOtherCalendarIsRejected(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

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

// TestMoveTodoAndJournalBetweenCalendarsOfOneICloudAccount is the todo
// and journal form of issue #828.
func TestMoveTodoAndJournalBetweenCalendarsOfOneICloudAccount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// create creates one item on calA and returns its UID. move moves
		// that item to calB.
		create func(t *testing.T, engine *Engine, calA int64) (uid string, move func(calB int64) error)
	}{
		{
			name: "todo",
			create: func(t *testing.T, engine *Engine, calA int64) (string, func(int64) error) {
				td, err := engine.todos.Create(context.Background(), todo.CreateParams{CalendarID: calA, Summary: "Buy milk"})
				if err != nil {
					t.Fatalf("Create todo: %v", err)
				}
				return td.UID, func(calB int64) error {
					_, err := engine.todos.Update(context.Background(), td.ID, todo.UpdateParams{
						CalendarID: calB, Summary: td.Summary, Status: td.Status, Class: td.Class,
					})
					return err
				}
			},
		},
		{
			name: "journal",
			create: func(t *testing.T, engine *Engine, calA int64) (string, func(int64) error) {
				j, err := engine.journals.Create(context.Background(), journal.CreateParams{CalendarID: calA, Summary: "Notes", StartDate: "2026-05-18"})
				if err != nil {
					t.Fatalf("Create journal: %v", err)
				}
				return j.UID, func(calB int64) error {
					_, err := engine.journals.Update(context.Background(), j.ID, journal.UpdateParams{
						CalendarID: calB, Summary: j.Summary, StartDate: "2026-05-18", Status: j.Status, Class: j.Class,
					})
					return err
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			engine, _, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()

			uid, move := tc.create(t, engine, calA)
			if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
				t.Fatalf("initial push = %+v, %v", res, err)
			}
			if err := move(calB); err != nil {
				t.Fatalf("Update %s (move): %v", tc.name, err)
			}
			syncPairClean(t, engine, calA, calB)
			wantOnlyServerObject(t, srv, "/cal/b/"+uid+".ics")
		})
	}
}

// TestEditLegacyOverrideInOtherCalendarKeepsCalendar proves that an edit
// of an override that an older version moved to another calendar still
// saves. The edit form preselects the calendar of the override, so the
// check must not refuse that calendar.
func TestEditLegacyOverrideInOtherCalendarKeepsCalendar(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	master, override, inst := createSeriesWithOverride(t, engine, calA)
	if _, err := engine.db.ExecContext(ctx, `UPDATE events SET calendar_id = ? WHERE id = ?`, calB, override.ID); err != nil {
		t.Fatalf("move override row: %v", err)
	}

	if _, err := engine.events.UpdateInstance(ctx, master.UID, inst, event.UpdateParams{
		CalendarID: calB, Title: "Later", StartTime: inst, EndTime: inst.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpdateInstance on the legacy override = %v, want nil", err)
	}
}

// TestEditLegacyOverrideBackToMasterCalendarReunitesSeries edits an
// override that an older version saved in calendar B, while the master
// stays in calendar A. The edit moves the override back to A. The move
// must delete the server object in B, or that stale copy keeps the UID
// and iCloud refuses the later PUT to A.
func TestEditLegacyOverrideBackToMasterCalendarReunitesSeries(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	master, override, inst := createSeriesWithOverride(t, engine, calA)
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 {
		t.Fatalf("initial push = %+v, %v", res, err)
	}
	// Seed the legacy state: the override row and a server object of the
	// UID live in B.
	if _, err := engine.db.ExecContext(ctx, `UPDATE events SET calendar_id = ? WHERE id = ?`, calB, override.ID); err != nil {
		t.Fatalf("move override row: %v", err)
	}
	pathA, pathB := "/cal/a/"+master.UID+".ics", "/cal/b/"+master.UID+".ics"
	srv.mu.Lock()
	srv.objects[pathB] = srv.objects[pathA]
	srv.etags[pathB] = `"legacy"`
	srv.mu.Unlock()
	var hrefA string
	if err := engine.db.QueryRowContext(ctx,
		`SELECT remote_url FROM sync_resources WHERE calendar_id = ? AND uid = ?`, calA, master.UID,
	).Scan(&hrefA); err != nil {
		t.Fatalf("read sync row of A: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty) VALUES (?, ?, 'event', ?, '"legacy"', 0)`,
		calB, master.UID, strings.Replace(hrefA, "/cal/a/", "/cal/b/", 1),
	); err != nil {
		t.Fatalf("seed sync row of B: %v", err)
	}

	if _, err := engine.events.UpdateInstance(ctx, master.UID, inst, event.UpdateParams{
		CalendarID: calA, Title: "Back", StartTime: inst, EndTime: inst.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpdateInstance back to the master calendar = %v, want nil", err)
	}
	if n := countRows(t, engine, "events", calB, master.UID); n != 0 {
		t.Fatalf("event rows left in calendar B = %d, want 0", n)
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, pathA)
}

// TestMoveTodoAndJournalOverrideRowIsRejected updates a todo override row
// and a journal override row by its ID with another calendar. One CalDAV
// object holds the whole series, so the override cannot move alone. Before
// the fix, the update moved the whole series in silence. An override that
// an older version saved in another calendar can still move back to the
// calendar of the master. A move of the master still moves the series.
func TestMoveTodoAndJournalOverrideRowIsRejected(t *testing.T) {
	t.Parallel()
	type item struct {
		table   string
		wantErr error
		// create creates a master and an override row on calA. It returns
		// the UID, the master ID, and the override ID.
		create func(t *testing.T, engine *Engine, calA int64) (uid string, masterID, overrideID int64)
		// move updates the row with id to calendar calID.
		move func(engine *Engine, id, calID int64) error
	}
	asOverride := func(t *testing.T, engine *Engine, table, uid string, id int64) {
		t.Helper()
		if _, err := engine.db.ExecContext(context.Background(),
			`UPDATE `+table+` SET uid = ?, recurrence_id = '2026-05-19T09:00:00Z', recurrence_rule = NULL WHERE id = ?`, uid, id,
		); err != nil {
			t.Fatalf("seed override row: %v", err)
		}
	}
	tests := []item{
		{
			table:   "todos",
			wantErr: todo.ErrInstanceCalendarMove,
			create: func(t *testing.T, engine *Engine, calA int64) (string, int64, int64) {
				ctx := context.Background()
				master, err := engine.todos.Create(ctx, todo.CreateParams{CalendarID: calA, Summary: "Water plants", DueDate: "2026-05-18T09:00:00Z", RecurrenceRule: "FREQ=DAILY;COUNT=5"})
				if err != nil {
					t.Fatalf("Create todo: %v", err)
				}
				other, err := engine.todos.Create(ctx, todo.CreateParams{CalendarID: calA, Summary: "Water plants late", DueDate: "2026-05-19T10:00:00Z"})
				if err != nil {
					t.Fatalf("Create todo override: %v", err)
				}
				asOverride(t, engine, "todos", master.UID, other.ID)
				return master.UID, master.ID, other.ID
			},
			move: func(engine *Engine, id, calID int64) error {
				td, err := engine.todos.Get(context.Background(), id)
				if err != nil {
					return err
				}
				_, err = engine.todos.Update(context.Background(), id, todo.UpdateParams{
					CalendarID: calID, Summary: td.Summary, DueDate: td.DueDate, RecurrenceRule: td.RecurrenceRule, Status: td.Status, Class: td.Class,
				})
				return err
			},
		},
		{
			table:   "journals",
			wantErr: journal.ErrInstanceCalendarMove,
			create: func(t *testing.T, engine *Engine, calA int64) (string, int64, int64) {
				ctx := context.Background()
				master, err := engine.journals.Create(ctx, journal.CreateParams{CalendarID: calA, Summary: "Log", StartDate: "2026-05-18", RecurrenceRule: "FREQ=DAILY;COUNT=5"})
				if err != nil {
					t.Fatalf("Create journal: %v", err)
				}
				other, err := engine.journals.Create(ctx, journal.CreateParams{CalendarID: calA, Summary: "Log late", StartDate: "2026-05-19"})
				if err != nil {
					t.Fatalf("Create journal override: %v", err)
				}
				asOverride(t, engine, "journals", master.UID, other.ID)
				return master.UID, master.ID, other.ID
			},
			move: func(engine *Engine, id, calID int64) error {
				j, err := engine.journals.Get(context.Background(), id)
				if err != nil {
					return err
				}
				_, err = engine.journals.Update(context.Background(), id, journal.UpdateParams{
					CalendarID: calID, Summary: j.Summary, StartDate: j.StartDate, RecurrenceRule: j.RecurrenceRule, Status: j.Status, Class: j.Class,
				})
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			t.Parallel()
			engine, _, _, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()

			uid, masterID, overrideID := tc.create(t, engine, calA)
			if err := tc.move(engine, overrideID, calB); !errors.Is(err, tc.wantErr) {
				t.Fatalf("move of an override row = %v, want %v", err, tc.wantErr)
			}
			if n := countRows(t, engine, tc.table, calA, uid); n != 2 {
				t.Fatalf("rows in calendar A = %d, want 2 (master and override)", n)
			}
			if n := countRows(t, engine, tc.table, calB, uid); n != 0 {
				t.Fatalf("rows in calendar B = %d, want 0", n)
			}
			if err := tc.move(engine, overrideID, calA); err != nil {
				t.Fatalf("edit of an override in its own calendar = %v, want nil", err)
			}

			// A legacy override in B can move back to the master in A.
			if _, err := engine.db.ExecContext(ctx, `UPDATE `+tc.table+` SET calendar_id = ? WHERE id = ?`, calB, overrideID); err != nil {
				t.Fatalf("seed legacy override: %v", err)
			}
			if err := tc.move(engine, overrideID, calA); err != nil {
				t.Fatalf("move of a legacy override back to the master = %v, want nil", err)
			}
			if n := countRows(t, engine, tc.table, calB, uid); n != 0 {
				t.Fatalf("rows left in calendar B = %d, want 0", n)
			}

			// A move of the master moves the whole series.
			if err := tc.move(engine, masterID, calB); err != nil {
				t.Fatalf("move of the master = %v, want nil", err)
			}
			if n := countRows(t, engine, tc.table, calB, uid); n != 2 {
				t.Fatalf("rows in calendar B after the master move = %d, want 2", n)
			}
		})
	}
}
