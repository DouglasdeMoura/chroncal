package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// The tests in this file check the rule for a local edit that no push sent
// against a remote delete of its object: the local edit wins, and the push
// creates the object again. A server-wins pass and a read-only calendar
// keep the remote delete (issue #866).

// wantRecreated checks that the server holds the object at path again with
// the title of the local edit, and that the sync row of uid is clean with
// that href and the new etag.
func wantRecreated(t *testing.T, q *storage.Queries, srv *icloudFakeServer, calendarID int64, uid, path, title string) {
	t.Helper()
	if !srv.has(path) || !strings.Contains(srv.body(path), "SUMMARY:"+title) {
		t.Fatalf("server object %s = %q; want the local edit %q", path, srv.body(path), title)
	}
	sr, err := q.GetSyncResource(context.Background(), storage.GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	if err != nil || sr.RemoteUrl != path || sr.Dirty != 0 || sr.Etag != strings.Trim(srv.etag(path), `"`) {
		t.Fatalf("sync resource = %+v, %v; want a clean row at %s with the new etag", sr, err, path)
	}
}

// wantLive checks that the event is live in calendarID with title.
func wantLive(t *testing.T, engine *Engine, id, calendarID int64, title string) {
	t.Helper()
	got, err := engine.events.Get(context.Background(), id)
	if err != nil || got.DeletedAt != nil || got.CalendarID != calendarID || got.Title != title {
		t.Fatalf("event = %+v, %v; want %q live in calendar %d", got, err, title, calendarID)
	}
}

// editTitle gives the event a new title, as a local edit does.
func editTitle(t *testing.T, engine *Engine, evt event.Event, title string) {
	t.Helper()
	if _, err := engine.events.Update(context.Background(), evt.ID, event.UpdateParams{
		Title: title, StartTime: evt.StartTime, EndTime: evt.EndTime, CalendarID: evt.CalendarID,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// TestUpdateOfAGoneObjectIsCreatedAgain deletes the object of a pushed
// event on the server and edits the event locally. The update gets 412,
// and the GET finds no object. The push must send a create at the same
// href in the same pass.
func TestUpdateOfAGoneObjectIsCreatedAgain(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.deleteRemote(path)
	editTitle(t, engine, evt, "Dentist (local)")

	putsBefore, _ := srv.counts()
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || res.Pushed != 1 || len(res.Errors) != 0 || res.Conflicts != 0 || len(res.Warnings) != 1 {
		t.Fatalf("push of A = %+v, %v; want one create after the refused update, with a warning", res, err)
	}
	if puts, _ := srv.counts(); puts != putsBefore+2 {
		t.Fatalf("PUTs = %d, want %d: the update and the create", puts, putsBefore+2)
	}
	wantRecreated(t, q, srv, calA, evt.UID, path, "Dentist (local)")
	if _, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil {
		t.Fatalf("pass of A: %v", err)
	}
	wantLive(t, engine, evt.ID, calA, "Dentist (local)")
}

// TestServerWinsPassKeepsTheRemoteDeleteOfAnEdit is the server-wins case of
// TestUpdateOfAGoneObjectIsCreatedAgain. The push sends no create, and the
// pull moves the event to the trash.
func TestServerWinsPassKeepsTheRemoteDeleteOfAnEdit(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.deleteRemote(path)
	editTitle(t, engine, evt, "Dentist (local)")

	putsBefore, _ := srv.counts()
	res, err := engine.SyncCalendar(ctx, calA, ConflictServerWins)
	if err != nil || res.Pushed != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A = %+v, %v; want no push and the remote delete applied", res, err)
	}
	if puts, _ := srv.counts(); puts != putsBefore+1 || srv.has(path) {
		t.Fatalf("PUTs = %d (want %d), object = %v; want no create", puts, putsBefore+1, srv.has(path))
	}
	if got, err := engine.events.Get(ctx, evt.ID); err == nil && got.DeletedAt == nil {
		t.Fatalf("event = %+v; want it in the trash", got)
	}
}

// TestPullKeepsAnUnpushedEditOfARemoteDelete applies a server-reported
// delete to a dirty row before any push. The row must stay without its
// href, and the next push must create the object again.
func TestPullKeepsAnUnpushedEditOfARemoteDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.deleteRemote(path)
	editTitle(t, engine, evt, "Dentist (local)")

	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	deletions := newPendingDeletions(engine.logger)
	deletions.markExplicit(sr)
	deleted, failed, warns := deletions.apply(ctx, engine, calA, nil)
	if deleted != 0 || failed != 0 || len(warns) != 1 {
		t.Fatalf("apply = %d deleted, %d failed, %v; want the edit kept with a warning", deleted, failed, warns)
	}
	wantLive(t, engine, evt.ID, calA, "Dentist (local)")
	after, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || after.RemoteUrl != "" || after.Etag != "" || after.Dirty != 1 || after.Rev != sr.Rev {
		t.Fatalf("sync resource = %+v, %v; want a dirty row without an href, with rev %d", after, err, sr.Rev)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Pushed != 1 || len(res.Errors) != 0 {
		t.Fatalf("push of A = %+v, %v; want one create", res, err)
	}
	wantRecreated(t, q, srv, calA, evt.UID, path, "Dentist (local)")
}

// TestPullTrashesAnEditOfAReadOnlyCalendar applies a server-reported delete
// to a dirty row of a read-only calendar. No push ever sends the edit, so
// the remote delete wins.
func TestPullTrashesAnEditOfAReadOnlyCalendar(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	srv.deleteRemote("/cal/a/" + evt.UID + ".ics")
	editTitle(t, engine, evt, "Dentist (local)")
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("make A read-only: %v", err)
	}

	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	deletions := newPendingDeletions(engine.logger)
	deletions.markExplicit(sr)
	if deleted, failed, _ := deletions.apply(ctx, engine, calA, nil); deleted != 1 || failed != 0 {
		t.Fatalf("apply = %d deleted, %d failed; want the remote delete applied", deleted, failed)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err == nil && got.DeletedAt == nil {
		t.Fatalf("event = %+v; want it in the trash", got)
	}
}

// TestRestoreFromTheTrashWinsOverARemoteDelete deletes a pushed event
// locally and restores it from the trash before a push. Another client
// deletes the object meanwhile. The pass must create the object again.
func TestRestoreFromTheTrashWinsOverARemoteDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := engine.events.RestoreByID(ctx, evt.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	srv.deleteRemote(path)

	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 0 {
		t.Fatalf("pass of A = %+v, %v; want the restore kept", res, err)
	}
	wantLive(t, engine, evt.ID, calA, evt.Title)
	wantRecreated(t, q, srv, calA, evt.UID, path, evt.Title)
}

// TestMoveBackWinsOverARemoteDelete moves a pushed event out of A and back
// before a push. Another client deletes the object in A meanwhile. The
// pass of A must create the object again, and B must hold no copy.
func TestMoveBackWinsOverARemoteDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	moveEvent(t, engine, evt, calB)
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, moved, calA)
	srv.deleteRemote(path)

	for _, cal := range []int64{calA, calB} {
		if res, err := engine.SyncCalendar(ctx, cal, ConflictPrompt); err != nil || len(res.Errors) != 0 {
			t.Fatalf("pass of %d = %+v, %v; want a clean pass", cal, res, err)
		}
	}
	wantLive(t, engine, evt.ID, calA, evt.Title)
	wantRecreated(t, q, srv, calA, evt.UID, path, evt.Title)
	if srv.has("/cal/b/" + evt.UID + ".ics") {
		t.Fatal("B holds a copy after the move back")
	}
}

// TestLocalPickOfAFailedCancelWinsOverARemoteDelete restores the local
// version of a failed cancel while the source object exists. Another
// client then deletes the object before the push. The push must create the
// object again with the restored version.
func TestLocalPickOfAFailedCancelWinsOverARemoteDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := failCancelOf(t, engine, q, srv, calA, calB)
	path := "/cal/a/" + evt.UID + ".ics"
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("local pick: %v", err)
	}
	srv.deleteRemote(path)

	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Pushed != 1 || len(res.Errors) != 0 {
		t.Fatalf("push of A = %+v, %v; want one create after the refused update", res, err)
	}
	wantLive(t, engine, evt.ID, calA, "Dentist")
	wantRecreated(t, q, srv, calA, evt.UID, path, "Dentist")
}
