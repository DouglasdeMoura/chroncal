package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// pullBody imports one VCALENDAR body into calendarID as a pull does.
func pullBody(t *testing.T, engine *Engine, calendarID int64, path, etag, components string) (bool, []ImportWarning) {
	t.Helper()
	data, err := parseICalData([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + components + "END:VCALENDAR\r\n"))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	_, imported, warnings, err := engine.importFetchedResource(context.Background(), calendarID, nil, fetchedResource{path: path, href: path, etag: etag, data: data})
	if err != nil {
		t.Fatalf("pull %s: %v", path, err)
	}
	return imported, warnings
}

const movedTodoBody = "BEGIN:VTODO\r\nUID:moved-todo\r\nDTSTAMP:20261020T000000Z\r\nSUMMARY:From B\r\nEND:VTODO\r\n"

// seedPushedTodo writes a todo in calendarID with a clean sync row, as a
// pushed item has.
func seedPushedTodo(t *testing.T, engine *Engine, calendarID int64, uid, path string) todo.Todo {
	t.Helper()
	ctx := context.Background()
	td, err := engine.todos.UpsertByUID(ctx, todo.UpsertParams{UID: uid, CalendarID: calendarID, Summary: "Before"})
	if err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if err := engine.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: uid, OwnerType: "todo", RemoteUrl: path, Etag: `"a1"`, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET dirty = 0 WHERE calendar_id = ? AND uid = ?`, calendarID, uid); err != nil {
		t.Fatalf("clear dirty: %v", err)
	}
	return td
}

// wantNoTrace checks that calendarID has no sync row and no tombstone of
// uid: a follow writes nothing for the server of the source.
func wantNoTrace(t *testing.T, q *storage.Queries, calendarID int64, uid string) {
	t.Helper()
	ctx := context.Background()
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: uid}); err == nil {
		t.Errorf("calendar %d keeps a sync row of %s", calendarID, uid)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calendarID); err != nil || len(ts) != 0 {
		t.Errorf("tombstones of calendar %d = %v, %v; want none", calendarID, ts, err)
	}
}

// TestPullFollowsARemoteMoveInOneAccountBeforeThePullOfTheSource pulls
// calendar B first. Another client moved the todo from server calendar A
// to B of the same account and kept its UID (issue #911). The local row
// still lives in A, so the pull of B moves it into B. The source gets no
// tombstone, because the server already moved the item.
func TestPullFollowsARemoteMoveInOneAccountBeforeThePullOfTheSource(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	td := seedPushedTodo(t, engine, calA, "moved-todo", "/cal/a/moved-todo.ics")
	if imported, warnings := pullBody(t, engine, calB, "/cal/b/moved-todo.ics", `"e1"`, movedTodoBody); !imported || len(warnings) != 0 {
		t.Fatalf("pull of B = %v, %+v; want an import with no warning", imported, warnings)
	}
	got, err := engine.todos.Get(ctx, td.ID)
	if err != nil || got.CalendarID != calB || got.Summary != "From B" || got.DeletedAt != nil {
		t.Fatalf("todo = %+v, %v; want it live in B with the server body", got, err)
	}
	if res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: "moved-todo"}); err != nil || res.Dirty != 0 || res.RemoteUrl != "/cal/b/moved-todo.ics" {
		t.Errorf("sync row of B = %+v, %v; want a clean row with the href of B", res, err)
	}
	wantNoTrace(t, q, calA, "moved-todo")
}

// TestPullFollowsARemoteMoveAfterThePullOfTheSource pulls calendar A first:
// the server object of A is gone, so the pull of A put the row in the trash
// and dropped its sync row. The pull of B then moves the row out of the
// trash of A, and the upsert brings it back live in B.
func TestPullFollowsARemoteMoveAfterThePullOfTheSource(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	td, err := engine.todos.UpsertByUID(ctx, todo.UpsertParams{UID: "moved-todo", CalendarID: calA, Summary: "In A"})
	if err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if err := q.SoftDeleteTodo(ctx, td.ID); err != nil {
		t.Fatalf("SoftDeleteTodo: %v", err)
	}
	if imported, _ := pullBody(t, engine, calB, "/cal/b/moved-todo.ics", `"e1"`, movedTodoBody); !imported {
		t.Fatal("pull of B did not import")
	}
	got, err := engine.todos.Get(ctx, td.ID)
	if err != nil || got.CalendarID != calB || got.DeletedAt != nil {
		t.Fatalf("todo = %+v, %v; want it live in B", got, err)
	}
	wantNoTrace(t, q, calA, "moved-todo")
}

// TestPullFollowsATrashedRowOfALocalCalendar moves a row from the trash of
// a local calendar: nothing there keeps the UID live.
func TestPullFollowsATrashedRowOfALocalCalendar(t *testing.T) {
	t.Parallel()
	engine, q, _, _, calB := newICloudPairEngine(t)
	ctx := context.Background()
	local, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	td, err := engine.todos.UpsertByUID(ctx, todo.UpsertParams{UID: "moved-todo", CalendarID: local.ID, Summary: "Local"})
	if err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if err := q.SoftDeleteTodo(ctx, td.ID); err != nil {
		t.Fatalf("SoftDeleteTodo: %v", err)
	}
	if imported, _ := pullBody(t, engine, calB, "/cal/b/moved-todo.ics", `"e1"`, movedTodoBody); !imported {
		t.Fatal("pull of B did not import")
	}
	if got, err := engine.todos.Get(ctx, td.ID); err != nil || got.CalendarID != calB || got.DeletedAt != nil {
		t.Fatalf("todo = %+v, %v; want it live in B", got, err)
	}
}

// TestPullHealsASplitSeries pulls into calendar B a todo series whose
// master an older version left in calendar A of the same account while its
// override lives in B. The pull moves the master into B, so the series is
// one again.
func TestPullHealsASplitSeries(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	master, err := engine.todos.UpsertByUID(ctx, todo.UpsertParams{UID: "split", CalendarID: calA, Summary: "Series", DueDate: "2026-10-26", RecurrenceRule: "FREQ=WEEKLY"})
	if err != nil {
		t.Fatalf("seed master: %v", err)
	}
	// An older version wrote the override into B on its own.
	if _, err := engine.db.ExecContext(ctx, `INSERT INTO todos (uid, calendar_id, summary, status, class, recurrence_id, due_date) VALUES ('split', ?, 'One', 'NEEDS-ACTION', 'PUBLIC', '2026-11-02T00:00:00Z', '2026-11-02')`, calB); err != nil {
		t.Fatalf("seed split override: %v", err)
	}
	body := "BEGIN:VTODO\r\nUID:split\r\nDTSTAMP:20261020T000000Z\r\nSUMMARY:Series\r\nDUE;VALUE=DATE:20261026\r\nRRULE:FREQ=WEEKLY\r\nEND:VTODO\r\n" +
		"BEGIN:VTODO\r\nUID:split\r\nDTSTAMP:20261020T000000Z\r\nRECURRENCE-ID;VALUE=DATE:20261102\r\nSUMMARY:One\r\nDUE;VALUE=DATE:20261102\r\nEND:VTODO\r\n"
	if imported, warnings := pullBody(t, engine, calB, "/cal/b/split.ics", `"e1"`, body); !imported || len(warnings) != 0 {
		t.Fatalf("pull of B = %v, %+v; want an import with no warning", imported, warnings)
	}
	if got, err := engine.todos.Get(ctx, master.ID); err != nil || got.CalendarID != calB {
		t.Fatalf("master = %+v, %v; want it in B", got, err)
	}
	var outside int
	if err := engine.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM todos WHERE uid = 'split' AND calendar_id != ?`, calB).Scan(&outside); err != nil || outside != 0 {
		t.Errorf("rows of the series outside B = %d, %v; want 0", outside, err)
	}
}

// TestPullSkipsALiveRowOfAnotherCalendarOnce pulls into calendar B a
// journal whose live row lives in a local calendar, and a todo whose live
// row lives in a calendar of another account. A move could go back and
// forth between two accounts that both hold the UID, so the pull skips
// each body, with one warning that names the calendar. A second pass of a
// full snapshot gives no new warning. B gets no sync row for the bodies.
func TestPullSkipsALiveRowOfAnotherCalendarOnce(t *testing.T) {
	t.Parallel()
	engine, q, _, _, calB := newICloudPairEngine(t)
	ctx := context.Background()
	local, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	other, err := q.CreateAccount(ctx, storage.CreateAccountParams{Name: "other", ServerUrl: "https://other.example", AuthType: "basic", Username: "u"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	foreign, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "foreign"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{ID: foreign.ID, AccountID: &other.ID, RemoteUrl: storage.StringToNullable("/other/")}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	jr, err := engine.journals.UpsertByUID(ctx, journal.UpsertParams{UID: "shared-journal", CalendarID: local.ID, Summary: "Local"})
	if err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	td := seedPushedTodo(t, engine, foreign.ID, "moved-todo", "/other/moved-todo.ics")
	cases := []struct{ uid, body, calendar string }{
		{"shared-journal", "BEGIN:VJOURNAL\r\nUID:shared-journal\r\nDTSTAMP:20261020T000000Z\r\nDTSTART;VALUE=DATE:20261021\r\nSUMMARY:From B\r\nEND:VJOURNAL\r\n", `"local"`},
		{"moved-todo", movedTodoBody, `"foreign"`},
	}
	for _, tc := range cases {
		path := "/cal/b/" + tc.uid + ".ics"
		imported, warnings := pullBody(t, engine, calB, path, `"e1"`, tc.body)
		if imported || len(warnings) != 1 || !strings.Contains(warnings[0].Message, tc.uid+" lives in calendar "+tc.calendar) {
			t.Fatalf("first pass of %s = %v, %+v; want a skip with one warning", tc.uid, imported, warnings)
		}
		if imported, warnings := pullBody(t, engine, calB, path, `"e1"`, tc.body); imported || len(warnings) != 0 {
			t.Fatalf("second pass of %s = %v, %+v; want a skip with no new warning", tc.uid, imported, warnings)
		}
		if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: tc.uid}); err == nil {
			t.Errorf("B has a sync row of the skipped %s", tc.uid)
		}
	}
	if got, err := engine.journals.Get(ctx, jr.ID); err != nil || got.CalendarID != local.ID {
		t.Errorf("journal = %+v, %v; want it in the local calendar", got, err)
	}
	if got, err := engine.todos.Get(ctx, td.ID); err != nil || got.CalendarID != foreign.ID {
		t.Errorf("todo = %+v, %v; want it in the calendar of the other account", got, err)
	}
}

// TestPullSkipsWhileTheSourceWaitsForItsPush keeps state of the source
// that a follow would lose: an unsent edit, and a delete that waits for its
// push. The pull skips the body and keeps the rows in A.
func TestPullSkipsWhileTheSourceWaitsForItsPush(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	edited := seedPushedTodo(t, engine, calA, "edited-todo", "/cal/a/edited-todo.ics")
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET dirty = 1 WHERE calendar_id = ? AND uid = 'edited-todo'`, calA); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}
	body := strings.ReplaceAll(movedTodoBody, "moved-todo", "edited-todo")
	if imported, warnings := pullBody(t, engine, calB, "/cal/b/edited-todo.ics", `"e1"`, body); imported || len(warnings) != 1 || !strings.Contains(warnings[0].Message, "an edit of it there waits for its push") {
		t.Fatalf("pull of the edited todo = %v, %+v; want a skip", imported, warnings)
	}
	if got, err := engine.todos.Get(ctx, edited.ID); err != nil || got.CalendarID != calA || got.Summary != "Before" {
		t.Errorf("edited todo = %+v, %v; want the edit kept in A", got, err)
	}

	deleted := seedPushedTodo(t, engine, calA, "deleted-todo", "/cal/a/deleted-todo.ics")
	if err := engine.todos.Delete(ctx, deleted.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	body = strings.ReplaceAll(movedTodoBody, "moved-todo", "deleted-todo")
	if imported, warnings := pullBody(t, engine, calB, "/cal/b/deleted-todo.ics", `"e1"`, body); imported || len(warnings) != 1 || !strings.Contains(warnings[0].Message, `deleted-todo is in the trash of calendar "a", and a delete or a move of it there waits for its push`) {
		t.Fatalf("pull of the deleted todo = %v, %+v; want a skip", imported, warnings)
	}
	if got, err := engine.todos.GetIncludingDeleted(ctx, deleted.ID); err != nil || got.CalendarID != calA || got.DeletedAt == nil {
		t.Errorf("deleted todo = %+v, %v; want it in the trash of A", got, err)
	}
}
