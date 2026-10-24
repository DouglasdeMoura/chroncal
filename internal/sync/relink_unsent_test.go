package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// unlinkAndEdit removes the account of calA as an account removal does, and
// edits the pushed event evt while the calendar has no account. It returns
// the account ID for the relink.
func unlinkAndEdit(t *testing.T, engine *Engine, q *storage.Queries, calA int64, title string) (accountID int64, evtID int64) {
	t.Helper()
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	cal, err := q.GetCalendar(ctx, calA)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	accountID = *cal.AccountID
	if err := q.ClearRemoteLinksByAccount(ctx, &accountID); err != nil {
		t.Fatalf("ClearRemoteLinksByAccount: %v", err)
	}
	editTitle(t, engine, evt, title)
	return accountID, evt.ID
}

// TestRelinkKeepsAnEditFromTheUnlinkedTime edits a pushed event while its
// calendar has no account, and the server edits the event too. After the
// relink, a server-wins pass must keep the local edit: it records an open
// conflict and the row stays dirty, and the calendar keeps relink_pending.
// Without the rule the pass adopts the server body (issue #876).
func TestRelinkKeepsAnEditFromTheUnlinkedTime(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	accountID, evtID := unlinkAndEdit(t, engine, q, calA, "Dentist (local)")
	evt, err := engine.events.Get(ctx, evtID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (server)")
	if err := calendar.NewService(engine.db, q).LinkToAccount(ctx, calA, accountID, "/cal/a/"); err != nil {
		t.Fatalf("LinkToAccount: %v", err)
	}
	// The fake server answers a sync-collection REPORT with a token only.
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET sync_token = 'tok0' WHERE id = ?`, calA); err != nil {
		t.Fatalf("seed sync token: %v", err)
	}

	for pass := 1; pass <= 2; pass++ {
		res, err := engine.SyncCalendar(ctx, calA, ConflictServerWins)
		if err != nil || res.AutoResolved != 0 {
			t.Fatalf("pass %d = %+v, %v; want no automatic resolution", pass, res, err)
		}
	}
	if n, err := q.CountOpenSyncConflicts(ctx, storage.CountOpenSyncConflictsParams{CalendarID: calA, Uid: evt.UID}); err != nil || n != 1 {
		t.Fatalf("open conflicts = %d, %v; want one", n, err)
	}
	if cal, err := q.GetCalendar(ctx, calA); err != nil || cal.RelinkPending != 1 {
		t.Fatalf("relink_pending = %d, %v; want 1 while the edit waits", cal.RelinkPending, err)
	}
	wantLive(t, engine, evtID, calA, "Dentist (local)")
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
		t.Fatalf("sync row = %+v, %v; want the edit still dirty", sr, err)
	}
}

// TestRelinkPendingClearsAfterACleanPass relinks a calendar with an edit
// from the time without an account, and no server change. The pass pushes
// the edit and clears relink_pending, so later passes use the configured
// strategy again (issue #876).
func TestRelinkPendingClearsAfterACleanPass(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	accountID, evtID := unlinkAndEdit(t, engine, q, calA, "Dentist (local)")
	if err := calendar.NewService(engine.db, q).LinkToAccount(ctx, calA, accountID, "/cal/a/"); err != nil {
		t.Fatalf("LinkToAccount: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET sync_token = 'tok0' WHERE id = ?`, calA); err != nil {
		t.Fatalf("seed sync token: %v", err)
	}
	res, err := engine.SyncCalendar(ctx, calA, ConflictServerWins)
	if err != nil || res.Pushed != 1 || len(res.Errors) != 0 {
		t.Fatalf("pass = %+v, %v; want the edit pushed", res, err)
	}
	evt, err := engine.events.Get(ctx, evtID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if body := srv.body("/cal/a/" + evt.UID + ".ics"); !strings.Contains(body, "SUMMARY:Dentist (local)") {
		t.Fatalf("server body = %q; want the edit", body)
	}
	if cal, err := q.GetCalendar(ctx, calA); err != nil || cal.RelinkPending != 0 {
		t.Fatalf("relink_pending = %d, %v; want 0 after the clean pass", cal.RelinkPending, err)
	}
}

// TestRelinkToAReadOnlyCollectionSettlesTheEdit edits a pushed event while
// its calendar has no account, and the collection is read-only at the
// relink. No push can send the edit, so the pass clears the mark and the
// etag, and the pull reads the server body (issue #876).
func TestRelinkToAReadOnlyCollectionSettlesTheEdit(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	accountID, evtID := unlinkAndEdit(t, engine, q, calA, "Dentist (local)")
	evt, err := engine.events.Get(ctx, evtID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
		t.Fatalf("sync row before the relink = %+v, %v; want the edit marked", sr, err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("set read-only: %v", err)
	}
	if err := calendar.NewService(engine.db, q).LinkToAccount(ctx, calA, accountID, "/cal/a/"); err != nil {
		t.Fatalf("LinkToAccount: %v", err)
	}
	cal, err := q.GetCalendar(ctx, calA)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if _, err := engine.relinkStrategy(ctx, cal, ConflictServerWins); err != nil {
		t.Fatalf("relinkStrategy: %v", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Dirty != 0 || sr.Etag != "" || sr.RemoteUrl == "" {
		t.Fatalf("sync row after the relink = %+v, %v; want a clean row with its href and no etag", sr, err)
	}
}

// TestNoMarkForACalendarWithALocalDirectory gives a calendar with no
// account a local directory. No link takes the calendar again, so an edit
// must not mark its sync row (issue #876).
func TestNoMarkForACalendarWithALocalDirectory(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	cal, err := q.GetCalendar(ctx, calA)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if err := q.ClearRemoteLinksByAccount(ctx, cal.AccountID); err != nil {
		t.Fatalf("ClearRemoteLinksByAccount: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET local_path = '/tmp/x' WHERE id = ?`, calA); err != nil {
		t.Fatalf("set local path: %v", err)
	}
	editTitle(t, engine, evt, "Dentist (local)")
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 0 {
		t.Fatalf("sync row = %+v, %v; want no mark", sr, err)
	}
}
