package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// pushMoveFixture is one synced event in MoveWork on a generic CalDAV
// stub, pushed once, then moved to MovePersonal by the service.
type pushMoveFixture struct {
	a                  *app.App
	stub               *testutil.GenericCalDAV
	workID, personalID int64
	moved              event.Event
}

func newPushMoveFixture(t *testing.T) pushMoveFixture {
	t.Helper()
	dbPath := setupCalendarCLITestEnv(t)
	stub := testutil.NewGenericCalDAV()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	ctx := context.Background()
	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "__push_move_test", ServerUrl: srv.URL, AuthType: "bearer", Username: "alice",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	link := func(name, path string) int64 {
		t.Helper()
		cal, err := a.Calendars.Create(ctx, name, "#7C3AED", "")
		if err != nil {
			t.Fatalf("calendar create %s: %v", name, err)
		}
		if err := a.Calendars.LinkToAccount(ctx, cal.ID, account.ID, srv.URL+path); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
		return cal.ID
	}
	workID, personalID := link("MoveWork", "/calendars/work/"), link("MovePersonal", "/calendars/personal/")
	a.AllowPlaintext = true
	credStore, err := auth.NewCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, true)
	if err != nil {
		t.Fatalf("credential store: %v", err)
	}
	if err := credStore.Set(auth.Credential{
		AccountID:          account.ID,
		AccountFingerprint: auth.AccountFingerprint(srv.URL, "bearer", "alice"),
		AccessToken:        "test-token",
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := a.Events.Create(ctx, event.CreateParams{CalendarID: workID, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("event create: %v", err)
	}
	var out, warn bytes.Buffer
	pushCalendarAfterWrite(a, workID, &out, &warn)
	if got := stub.Paths(); len(got) != 1 || !strings.HasPrefix(got[0], "/calendars/work/") {
		t.Fatalf("server objects after the first push = %v, want one in work", got)
	}
	moved, err := a.Events.Update(ctx, evt.ID, event.UpdateParams{
		Title: evt.Title, StartTime: evt.StartTime, EndTime: evt.EndTime, CalendarID: personalID,
		Status: evt.Status, Transp: evt.Transp, Class: evt.Class,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	return pushMoveFixture{a: a, stub: stub, workID: workID, personalID: personalID, moved: moved}
}

func (f pushMoveFixture) pushMove(t *testing.T) (out, warn string) {
	t.Helper()
	var o, w bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&o)
	cmd.SetErr(&w)
	opportunisticPushMove(f.a, f.workID, f.moved.CalendarID, f.moved.UID, cmd)
	return o.String(), w.String()
}

// TestMoveWritePathPushDeletesSourceObject moves a synced event from Work
// to Personal on a server that keeps a UID unique per collection. The
// write-path push must send the source delete at once. Before the fix the
// Work object stayed on the server until the next full sync, so the event
// showed in both calendars.
func TestMoveWritePathPushDeletesSourceObject(t *testing.T) {
	f := newPushMoveFixture(t)
	out, warn := f.pushMove(t)
	got := f.stub.Paths()
	if len(got) != 1 || !strings.HasPrefix(got[0], "/calendars/personal/") {
		t.Fatalf("server objects after the move push = %v, want only one in personal (out %q, warn %q)", got, out, warn)
	}
}

// TestMoveWritePathPushKeepsSourceWhenTargetFails moves a synced event, and
// the PUT to the target calendar fails. The source delete must not run:
// the source object is then the only server copy. The source tombstone
// stays, so a later push completes the move.
func TestMoveWritePathPushKeepsSourceWhenTargetFails(t *testing.T) {
	f := newPushMoveFixture(t)
	// 507 is not transient, so the push does not wait for a retry backoff.
	f.stub.SetPutStatus("/calendars/personal/", http.StatusInsufficientStorage)
	f.pushMove(t)

	got := f.stub.Paths()
	if len(got) != 1 || !strings.HasPrefix(got[0], "/calendars/work/") {
		t.Fatalf("server objects after a failed target push = %v, want the work object kept", got)
	}
	ts, err := f.a.Queries.ListTombstonesByCalendar(context.Background(), f.workID)
	if err != nil || len(ts) != 1 {
		t.Fatalf("tombstones of work = %v, %v; want one kept", ts, err)
	}
}

// resolveMoveJSON runs sync resolve with --output json in a child process
// and decodes its object. It fails the test on a nonzero exit.
func resolveMoveJSON(t *testing.T, conflictID int64, pick string) map[string]any {
	t.Helper()
	stdout, stderr, err := runChroncalCommand(t,
		"--allow-plaintext", "sync", "resolve", strconv.FormatInt(conflictID, 10), "--pick", pick, "--output", "json")
	if err != nil {
		t.Fatalf("sync resolve = %v, want exit 0 (stderr: %s)", err, stderr)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(stdout), &item); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	return item
}

// TestSyncResolveOfAMoveWithoutItsSourceHalfExitsZero records a move
// conflict whose source row and tombstone are both gone. The server pick
// finishes the move as a defensive step. The command exits with 0, and the
// JSON names the finished outcome with picked none.
func TestSyncResolveOfAMoveWithoutItsSourceHalfExitsZero(t *testing.T) {
	f := newPushMoveFixture(t)
	ctx := context.Background()
	q := f.a.Queries
	if err := q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: f.workID, Uid: f.moved.UID}); err != nil {
		t.Fatalf("DeleteSyncResource: %v", err)
	}
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{CalendarID: f.workID, Uid: f.moved.UID}); err != nil {
		t.Fatalf("drop the tombstone: %v", err)
	}
	source := f.workID
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: f.personalID, OwnerType: "event", OwnerID: f.moved.ID, Uid: f.moved.UID,
		LocalIcal: "local", ServerIcal: "server", ServerEtag: "e1", MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	conflicts, err := q.ListSyncConflicts(ctx)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("ListSyncConflicts = %d, %v; want 1", len(conflicts), err)
	}
	item := resolveMoveJSON(t, conflicts[0].ID, "server")
	if item["outcome"] != "finished" || item["picked"] != "none" || item["note"] != "the source is gone, so the move finished; the next sync creates the moved copy" {
		t.Fatalf("json = %v, want the finished outcome", item)
	}
}

// TestSyncResolveReplayOfAGoneSourceRefuses leaves a failed cancel with its
// rows back in the source, and the source object is gone from the server.
// The replay of the server pick cannot complete the cancel. The command
// fails with the refusal, and the conflict stays a failed cancel.
func TestSyncResolveReplayOfAGoneSourceRefuses(t *testing.T) {
	f := newPushMoveFixture(t)
	ctx := context.Background()
	q := f.a.Queries
	db := f.a.DB
	work, err := q.GetCalendar(ctx, f.workID)
	if err != nil {
		t.Fatalf("GetCalendar work: %v", err)
	}
	gone := storage.NullableToString(work.RemoteUrl) + "gone.ics"
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE events SET calendar_id = ? WHERE uid = ?`, []any{f.workID, f.moved.UID}},
		{`DELETE FROM tombstones WHERE uid = ?`, []any{f.moved.UID}},
		{`UPDATE sync_resources SET remote_url = ?, etag = 'e1', dirty = 0 WHERE calendar_id = ? AND uid = ?`, []any{gone, f.workID, f.moved.UID}},
	} {
		if _, err := db.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed %q: %v", stmt.sql, err)
		}
	}
	source := f.workID
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: f.personalID, OwnerType: "event", OwnerID: f.moved.ID, Uid: f.moved.UID,
		LocalIcal:  "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\nBEGIN:VEVENT\r\nUID:" + f.moved.UID + "\r\nDTSTAMP:20260403T100000Z\r\nDTSTART:20260403T100000Z\r\nDTEND:20260403T110000Z\r\nSUMMARY:Dentist\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		ServerIcal: "server", ServerEtag: "e1", MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sync_conflicts SET move_state = 'cancel-failed' WHERE uid = ?`, f.moved.UID); err != nil {
		t.Fatalf("fail the cancel: %v", err)
	}
	conflicts, err := q.ListSyncConflicts(ctx)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("ListSyncConflicts = %d, %v; want 1", len(conflicts), err)
	}
	_, stderr, err := runChroncalCommand(t,
		"--allow-plaintext", "sync", "resolve", strconv.FormatInt(conflicts[0].ID, 10), "--pick", "server")
	if err == nil || !strings.Contains(stderr, "so the cancel cannot complete; the item stays as it is") {
		t.Fatalf("sync resolve = %v (stderr %q), want the refusal", err, stderr)
	}
	row, err := q.GetSyncConflict(ctx, conflicts[0].ID)
	if err != nil || row.MoveState != "cancel-failed" || row.ResolvedAt != nil {
		t.Fatalf("conflict = %+v, %v; want an open failed cancel", row, err)
	}
	// The local pick ends the failed cancel. Its note goes into the JSON.
	item := resolveMoveJSON(t, conflicts[0].ID, "local")
	if item["outcome"] != "resolved" || item["picked"] != "local" || item["note"] != "the source object is gone, so the next sync creates the item in the source calendar" {
		t.Fatalf("json = %v, want the local pick with the gone-source note", item)
	}
}
