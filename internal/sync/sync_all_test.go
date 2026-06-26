package sync

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// fakeCalDAVServer is a minimal CalDAV endpoint that answers the handful of
// requests one no-conflict SyncCalendar cycle makes: PROPFIND (calendar
// colour), REPORT (sync-collection), and PUT (push). It records per-method
// hit counts so a test can prove a calendar was actually contacted.
type fakeCalDAVServer struct {
	srv      *httptest.Server
	propfind atomic.Int64
	report   atomic.Int64
	put      atomic.Int64
}

func newFakeCalDAVServer(t *testing.T) *fakeCalDAVServer {
	t.Helper()
	f := &fakeCalDAVServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPFIND":
			f.propfind.Add(1)
			// Empty colour: matches the unset remote_color so the metadata
			// phase performs no write.
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>` + r.URL.Path + `</d:href>
    <d:propstat>
      <d:prop><ic:calendar-color></ic:calendar-color></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`))
		case "REPORT":
			f.report.Add(1)
			// Empty incremental sync-collection: no changes, fresh token.
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:sync-token>tok-next</d:sync-token>
</d:multistatus>`))
		case "PUT":
			f.put.Add(1)
			w.Header().Set("ETag", `"etag-pushed"`)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// newFileTestEngine builds an Engine backed by a temp-file SQLite database so
// every pooled connection shares state — concurrent SyncAll workers really do
// hit the same database, which is what -race needs to be meaningful. The
// in-memory test DB gives each connection its own private database, so it
// cannot exercise the concurrent path.
func newFileTestEngine(t *testing.T, credStore auth.CredentialStore) (*Engine, *sql.DB, *storage.Queries) {
	t.Helper()
	db, q, err := storage.Open(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatalf("open file test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewEngine(db, q,
		credStore,
		calendar.NewService(db, q),
		event.NewService(db, q),
		todo.NewService(db, q),
		journal.NewService(db, q),
		nil,
	), db, q
}

// seedSyncedCalendar creates an account+credential pointing at srv, then links
// a calendar to it. See linkDirtyCalendar for the per-calendar setup. Returns
// the new calendar ID.
func seedSyncedCalendar(t *testing.T, db *sql.DB, q *storage.Queries, credStore *mockCredStore, name string, srv *fakeCalDAVServer) int64 {
	t.Helper()
	account, err := q.CreateAccount(context.Background(), storage.CreateAccountParams{
		Name:      name,
		ServerUrl: srv.srv.URL,
		AuthType:  "basic",
		Username:  "user-" + name,
	})
	if err != nil {
		t.Fatalf("CreateAccount %s: %v", name, err)
	}
	credStore.creds[account.ID] = auth.Credential{AccountID: account.ID, Username: "user-" + name, Password: "pw"}
	return linkDirtyCalendar(t, db, q, name, account.ID, srv)
}

// linkDirtyCalendar attaches a calendar to an existing account with one dirty
// event ready to push and a stored sync-token (so pull runs incrementally and
// does not infer absence deletions). Reused to add a second calendar to an
// account, covering the same-account-serial branch.
func linkDirtyCalendar(t *testing.T, db *sql.DB, q *storage.Queries, name string, accountID int64, srv *fakeCalDAVServer) int64 {
	t.Helper()
	ctx := context.Background()

	cal, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
	if err != nil {
		t.Fatalf("CreateCalendar %s: %v", name, err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        cal.ID,
		AccountID: &accountID,
		RemoteUrl: storage.StringToNullable(srv.srv.URL + "/cal/" + name + "/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount %s: %v", name, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE calendars SET sync_token = 'tok0' WHERE id = ?`, cal.ID); err != nil {
		t.Fatalf("seed sync token %s: %v", name, err)
	}

	uid := "evt-" + name
	insertTestEvent(t, db, cal.ID, uid)
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   cal.ID,
		Uid:          uid,
		OwnerType:    "event",
		RemoteUrl:    "/cal/" + name + "/" + uid + ".ics",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource %s: %v", name, err)
	}
	return cal.ID
}

// accountIDForCalendar reads the account_id linked to a calendar.
func accountIDForCalendar(t *testing.T, q *storage.Queries, calendarID int64) int64 {
	t.Helper()
	cal, err := q.GetCalendar(context.Background(), calendarID)
	if err != nil {
		t.Fatalf("GetCalendar %d: %v", calendarID, err)
	}
	if cal.AccountID == nil {
		t.Fatalf("calendar %d not linked to an account", calendarID)
	}
	return *cal.AccountID
}

// TestSyncAllSyncsEveryConnectedCalendar drives SyncAll across multiple
// accounts (each its own server) plus a second calendar sharing one account and
// an unlinked calendar that must be skipped. It asserts every connected
// calendar is synced exactly once, results come back in ListCalendars order
// regardless of which worker finishes first, and each server was contacted.
// Run under -race this also guards the concurrent path against data races on
// the shared database and engine.
func TestSyncAllSyncsEveryConnectedCalendar(t *testing.T) {
	t.Parallel()

	credStore := &mockCredStore{creds: make(map[int64]auth.Credential)}
	engine, db, q := newFileTestEngine(t, credStore)

	ctx := context.Background()

	srvA := newFakeCalDAVServer(t)
	srvB := newFakeCalDAVServer(t)
	srvC := newFakeCalDAVServer(t)

	// account A: two calendars (same-account-serial branch).
	calA1 := seedSyncedCalendar(t, db, q, credStore, "aaa", srvA)
	acctA := accountIDForCalendar(t, q, calA1)
	calA2 := linkDirtyCalendar(t, db, q, "aab", acctA, srvA)
	// accounts B and C: one calendar each (cross-account-concurrent branch).
	calB := seedSyncedCalendar(t, db, q, credStore, "bbb", srvB)
	calC := seedSyncedCalendar(t, db, q, credStore, "ccc", srvC)
	// An unlinked calendar must be ignored entirely.
	if _, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "zzz-unlinked"}); err != nil {
		t.Fatalf("CreateCalendar unlinked: %v", err)
	}

	results, err := engine.SyncAll(ctx, ConflictServerWins)
	if err != nil {
		t.Fatalf("SyncAll: %v", err)
	}

	// ListCalendars orders by display_order, so results must match creation
	// order of the connected calendars, deterministically.
	wantOrder := []int64{calA1, calA2, calB, calC}
	if len(results) != len(wantOrder) {
		t.Fatalf("results = %d, want %d", len(results), len(wantOrder))
	}
	for i, want := range wantOrder {
		if results[i] == nil {
			t.Fatalf("results[%d] is nil", i)
		}
		if results[i].CalendarID != want {
			t.Fatalf("results[%d].CalendarID = %d, want %d", i, results[i].CalendarID, want)
		}
		if len(results[i].Errors) != 0 {
			t.Fatalf("results[%d] errors = %v, want none", i, results[i].Errors)
		}
		if results[i].Pushed != 1 {
			t.Fatalf("results[%d].Pushed = %d, want 1", i, results[i].Pushed)
		}
	}

	// Every server saw its calendars' pushes (account A served two calendars).
	for name, want := range map[string]struct {
		srv  *fakeCalDAVServer
		puts int64
	}{
		"A": {srvA, 2}, "B": {srvB, 1}, "C": {srvC, 1},
	} {
		if got := want.srv.put.Load(); got != want.puts {
			t.Fatalf("server %s PUTs = %d, want %d", name, got, want.puts)
		}
		if want.srv.report.Load() == 0 {
			t.Fatalf("server %s saw no REPORT (pull never ran)", name)
		}
	}
}

// TestSyncAllAggregatesPerCalendarErrors verifies a single failing calendar is
// captured in its own SyncResult without aborting the others, and that ordering
// is preserved.
func TestSyncAllAggregatesPerCalendarErrors(t *testing.T) {
	t.Parallel()

	credStore := &mockCredStore{creds: make(map[int64]auth.Credential)}
	engine, db, q := newFileTestEngine(t, credStore)

	ctx := context.Background()

	srvOK := newFakeCalDAVServer(t)
	calOK := seedSyncedCalendar(t, db, q, credStore, "good", srvOK)

	// A linked calendar whose account has no stored credential: loadCalendarClient
	// fails, so SyncCalendar returns an error that SyncAll must record without
	// derailing the healthy calendar.
	badAccount, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "bad",
		ServerUrl: "https://unreachable.invalid",
		AuthType:  "basic",
		Username:  "user-bad",
	})
	if err != nil {
		t.Fatalf("CreateAccount bad: %v", err)
	}
	badCal, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "zzz-bad"})
	if err != nil {
		t.Fatalf("CreateCalendar bad: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        badCal.ID,
		AccountID: &badAccount.ID,
		RemoteUrl: storage.StringToNullable("https://unreachable.invalid/cal/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount bad: %v", err)
	}

	results, err := engine.SyncAll(ctx, ConflictServerWins)
	if err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	// "good" sorts before "zzz-bad" by name, so order is deterministic.
	if results[0].CalendarID != calOK {
		t.Fatalf("results[0].CalendarID = %d, want %d", results[0].CalendarID, calOK)
	}
	if len(results[0].Errors) != 0 || results[0].Pushed != 1 {
		t.Fatalf("results[0] = %+v, want clean push", results[0])
	}
	if results[1].CalendarID != badCal.ID {
		t.Fatalf("results[1].CalendarID = %d, want %d", results[1].CalendarID, badCal.ID)
	}
	if len(results[1].Errors) == 0 {
		t.Fatalf("results[1] should carry the load error, got %+v", results[1])
	}
}
