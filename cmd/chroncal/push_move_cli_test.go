package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
