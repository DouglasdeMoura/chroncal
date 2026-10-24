package sync

import (
	"context"
	"errors"
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

// TestPullFollowsARemoteMoveBeforeThePullOfTheSource pulls calendar B
// first. Another client moved the todo from server calendar A to B and
// kept its UID (issue #911). The local row still lives in A, so the pull
// of B moves it into B by the rules of a calendar move, and the item stays
// live.
func TestPullFollowsARemoteMoveBeforeThePullOfTheSource(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	td, err := engine.todos.UpsertByUID(ctx, todo.UpsertParams{UID: "moved-todo", CalendarID: calA, Summary: "In A"})
	if err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if imported, warnings := pullBody(t, engine, calB, "/cal/b/moved-todo.ics", `"e1"`, movedTodoBody); !imported || len(warnings) != 0 {
		t.Fatalf("pull of B = %v, %+v; want an import with no warning", imported, warnings)
	}
	got, err := engine.todos.Get(ctx, td.ID)
	if err != nil || got.CalendarID != calB || got.Summary != "From B" || got.DeletedAt != nil {
		t.Fatalf("todo = %+v, %v; want it live in B with the server body", got, err)
	}
	if res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: "moved-todo"}); err != nil || res.Dirty != 0 {
		t.Errorf("sync row of B = %+v, %v; want a clean row", res, err)
	}
}

// TestPullFollowsARemoteMoveAfterThePullOfTheSource pulls calendar A first:
// the server object of A is gone, so the pull of A puts the row in the
// trash. The pull of B then moves the row out of the trash of A, and the
// upsert brings it back live in B.
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
}

// TestPullHealsASplitSeries pulls into calendar B a todo series whose
// master an older version left in calendar A while its override lives in
// B. The pull moves the master into B, so the series is one again.
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
	got, err := engine.todos.Get(ctx, master.ID)
	if err != nil || got.CalendarID != calB {
		t.Fatalf("master = %+v, %v; want it in B", got, err)
	}
	var outside int
	if err := engine.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM todos WHERE uid = 'split' AND calendar_id != ?`, calB).Scan(&outside); err != nil || outside != 0 {
		t.Errorf("rows of the series outside B = %d, %v; want 0", outside, err)
	}
}

// TestPullWaitsWhileTheSourceKeepsItsServerCopy pulls into calendar B a
// journal whose rows live in calendar A, while A still has a server copy
// of the UID. A follow would delete that copy through the move tombstone,
// so the pull refuses the body for now: a warning names the cause, the
// failure withholds the sync-token, and the rows stay in A. Once the pull
// of A drops its server copy, the next pull of B follows.
func TestPullWaitsWhileTheSourceKeepsItsServerCopy(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	jr, err := engine.journals.UpsertByUID(ctx, journal.UpsertParams{UID: "shared-journal", CalendarID: calA, Summary: "In A"})
	if err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calA, Uid: "shared-journal", OwnerType: "journal", RemoteUrl: "/cal/a/shared-journal.ics", Etag: `"a1"`, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	body := "BEGIN:VJOURNAL\r\nUID:shared-journal\r\nDTSTAMP:20261020T000000Z\r\nDTSTART;VALUE=DATE:20261021\r\nSUMMARY:From B\r\nEND:VJOURNAL\r\n"
	data, err := parseICalData([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + body + "END:VCALENDAR\r\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	path := "/cal/b/shared-journal.ics"
	_, imported, warnings, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: path, href: path, etag: `"e1"`, data: data})
	if !errors.Is(err, storage.ErrFollowRefused) || imported {
		t.Fatalf("pull = %v, %v; want a refused follow", imported, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "also has a copy on the server") {
		t.Fatalf("warnings = %+v; want the cause", warnings)
	}
	if got, err := engine.journals.Get(ctx, jr.ID); err != nil || got.CalendarID != calA || got.Summary != "In A" {
		t.Fatalf("journal = %+v, %v; want it unchanged in A", got, err)
	}

	// The pull of A finds the server copy gone and drops its sync row.
	if err := q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calA, Uid: "shared-journal"}); err != nil {
		t.Fatalf("DeleteSyncResource: %v", err)
	}
	if imported, warnings := pullBody(t, engine, calB, path, `"e1"`, body); !imported || len(warnings) != 0 {
		t.Fatalf("second pull = %v, %+v; want the follow", imported, warnings)
	}
	if got, err := engine.journals.Get(ctx, jr.ID); err != nil || got.CalendarID != calB || got.Summary != "From B" {
		t.Errorf("journal = %+v, %v; want it in B", got, err)
	}
}

// TestPullWaitsForAnUnpushedEditOfTheSource keeps an unsent local edit of
// the source calendar. The follow would replace it with the server body
// and clear its dirty flag, so the pull refuses the body for now.
func TestPullWaitsForAnUnpushedEditOfTheSource(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	td, err := engine.todos.UpsertByUID(ctx, todo.UpsertParams{UID: "moved-todo", CalendarID: calA, Summary: "Edited in A"})
	if err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calA, Uid: "moved-todo", OwnerType: "todo", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	data, err := parseICalData([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + movedTodoBody + "END:VCALENDAR\r\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	path := "/cal/b/moved-todo.ics"
	if _, imported, _, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: path, href: path, etag: `"e1"`, data: data}); !errors.Is(err, storage.ErrFollowRefused) || imported {
		t.Fatalf("pull = %v, %v; want a refused follow", imported, err)
	}
	if got, err := engine.todos.Get(ctx, td.ID); err != nil || got.CalendarID != calA || got.Summary != "Edited in A" {
		t.Errorf("todo = %+v, %v; want the edit kept in A", got, err)
	}
}
