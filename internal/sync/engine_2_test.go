package sync

import (
	"bytes"
	"context"

	"fmt"
	"io"

	"net/http"
	"net/http/httptest"

	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"

	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"

	"github.com/douglasdemoura/chroncal/internal/storage"

	"github.com/douglasdemoura/chroncal/internal/todo"
)

func TestEnginePullSkipsOpenConflict(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	const uid = "open-conflict-uid"
	insertTestEvent(t, db, calendarID, uid)

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          uid,
		OwnerType:    "event",
		RemoteUrl:    "/calendar/open-conflict.ics",
		Etag:         `"etag-old"`,
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calendarID,
		OwnerType:  "event",
		Uid:        uid,
		LocalIcal:  "local body",
		ServerIcal: "server body",
		ServerEtag: `"etag-server"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	const responseBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/open-conflict.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-server&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/abc</d:sync-token>
</d:multistatus>`

	// The multiget serves a server version that is newer than the recorded
	// one. The skip must refresh the open row with this body (issue #610).
	const fetchBody = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:open-conflict-uid
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Server version v2
END:VEVENT
END:VCALENDAR
`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "REPORT" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		body := responseBody
		if strings.Contains(string(raw), "calendar-multiget") {
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/open-conflict.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-server-v2&quot;</d:getetag>
        <cal:calendar-data>` + fetchBody + `</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.pulled != 0 {
		t.Fatalf("pulled = %d, want 0", result.pulled)
	}

	evt, err := q.GetEventByUID(ctx, uid)
	if err != nil {
		t.Fatalf("GetEventByUID: %v", err)
	}
	if evt.Title != "Test "+uid {
		t.Fatalf("title after pull = %q, want the local title", evt.Title)
	}
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Fatalf("Dirty = %d, want 1", res.Dirty)
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open conflicts = %d, want 1", len(open))
	}

	// The row holds the freshest server version, not the one from conflict
	// time. A later "sync resolve" then picks current server data, and the
	// sync-token may advance because the row carries the obligation.
	if open[0].ServerEtag != "etag-server-v2" {
		t.Fatalf("conflict ServerEtag = %q, want the refreshed etag-server-v2", open[0].ServerEtag)
	}
	if !strings.Contains(open[0].ServerIcal, "SUMMARY:Server version v2") {
		t.Fatalf("conflict ServerIcal = %q, want the refreshed v2 body", open[0].ServerIcal)
	}
	if open[0].LocalIcal != "local body" {
		t.Fatalf("conflict LocalIcal = %q, want the untouched recorded local body", open[0].LocalIcal)
	}
}

// TestEnginePullToleratesMultigetMissingPath verifies that a per-resource
// 404 returned by calendar-multiget after sync-collection nominated the path
// no longer aborts the whole pull. Resources that remain still import. Paths
// that 404 are NOT soft-deleted. A 404 here can be a transient server quirk,
// not a real deletion. We lost real user data the one time we tried that.
// The sync-token is held back. The next sync then re-lists the same change
// set and gets another chance to fetch the bodies that 404'd.
func TestEnginePullToleratesMultigetMissingPath(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "racey-deleted")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "racey-deleted",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/racey-deleted.ics",
		Etag:         "etag-old",
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/alive.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-alive&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/calendar/racey-deleted.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-stale&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/post-race</d:sync-token>
</d:multistatus>`

	const aliveICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:alive-uid
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Alive
END:VEVENT
END:VCALENDAR
`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "REPORT" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		if !strings.Contains(string(raw), "calendar-multiget") {
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Status:     "207 Multi-Status",
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body:       io.NopCloser(strings.NewReader(syncBody)),
				Request:    r,
			}, nil
		}
		multigetBody := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/alive.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-alive&quot;</d:getetag>
        <cal:calendar-data>` + aliveICS + `</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/calendar/racey-deleted.ics</d:href>
    <d:status>HTTP/1.1 404 Not Found</d:status>
  </d:response>
</d:multistatus>`
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(multigetBody)),
			Request:    r,
		}, nil
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.pulled != 1 {
		t.Fatalf("pulled = %d, want 1 (alive event)", result.pulled)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (multiget 404 must NOT soft-delete)", result.deleted)
	}

	// The "racey-deleted" event must still exist locally — multiget 404 is
	// not enough evidence to remove user data.
	if _, err := q.GetEventByUID(ctx, "racey-deleted"); err != nil {
		t.Fatalf("racey-deleted was unexpectedly deleted: %v", err)
	}
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "racey-deleted"})
	if err != nil {
		t.Fatalf("racey-deleted sync_resource was unexpectedly removed: %v", err)
	}
	if res.Etag != "etag-old" {
		t.Fatalf("racey-deleted etag = %q, want etag-old preserved", res.Etag)
	}

	// Sync-token is held back so the next sync re-lists and retries the
	// missing path.
	calRow, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if tok := storage.NullableToString(calRow.SyncToken); tok != "" {
		t.Fatalf("sync_token = %q, want empty (held back due to multiget miss)", tok)
	}
}

// TestEnginePullIncompletePullMarksCalendarUnhealthy reproduces issue #293. A
// pull that can never converge used to only log and return no error. Here the
// server keeps an href as changed. That href 404s on every multiget.
// SyncResult.Errors stayed empty. updateSyncHealth recorded the
// calendar as healthy. The ambient ⚠ glyph never lit up despite a stuck
// sync. The incomplete pull must surface an error. The calendar is then
// recorded unhealthy.
func TestEnginePullIncompletePullMarksCalendarUnhealthy(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "stuck-uid")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "stuck-uid",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/stuck.ics",
		Etag:         "etag-old",
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// The server reports stuck.ics as changed (new etag) but 404s it on
	// multiget — a multiget miss that never converges.
	const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/stuck.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-new&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/stuck</d:sync-token>
</d:multistatus>`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "REPORT" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		if !strings.Contains(string(raw), "calendar-multiget") {
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Status:     "207 Multi-Status",
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body:       io.NopCloser(strings.NewReader(syncBody)),
				Request:    r,
			}, nil
		}
		multigetBody := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/stuck.ics</d:href>
    <d:status>HTTP/1.1 404 Not Found</d:status>
  </d:response>
</d:multistatus>`
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(multigetBody)),
			Request:    r,
		}, nil
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(result.errors) == 0 {
		t.Fatal("incomplete pull surfaced no error: SyncResult.Errors stays empty and the calendar is recorded healthy")
	}

	// Mirror SyncCalendar's health bookkeeping: an incomplete pull's errors
	// flow into SyncResult.Errors, which updateSyncHealth uses to decide
	// healthy vs. stuck.
	sr := &SyncResult{CalendarID: calendarID}
	sr.Errors = append(sr.Errors, result.errors...)
	attemptedAt := time.Now().UTC().Format(time.RFC3339)
	if err := engine.updateSyncHealth(ctx, calendarID, attemptedAt, sr, nil); err != nil {
		t.Fatalf("updateSyncHealth: %v", err)
	}

	calRow, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if got := storage.NullableToString(calRow.LastSyncError); got == "" {
		t.Fatal("LastSyncError empty: a permanently stuck calendar still shows healthy")
	}
	if got := storage.NullableToString(calRow.LastSyncAt); got != "" {
		t.Fatalf("LastSyncAt = %q, want empty for an unconverged pull", got)
	}
}

// TestEngineSyncCalendarRecordsHealthOnEarlyClientFailure is the regression
// test for issue #416. When loadCalendarClient returns early (credentials
// gone, no linked account, empty RemoteUrl) the updateSyncHealth defer
// used to be registered after that call. It then never ran. LastSyncAttemptedAt
// / LastSyncError stayed stale. The ambient ⚠ sidebar glyph (which keys on a
// non-empty LastSyncError) stayed dark while the calendar was permanently
// failed. Notably OAuth calendars with revoked credentials.
func TestEngineSyncCalendarRecordsHealthOnEarlyClientFailure(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	// The default calendar has no linked account, so loadCalendarClient
	// returns early before any sync phase runs.
	if _, err := engine.SyncCalendar(ctx, calendarID, ConflictServerWins); err == nil {
		t.Fatal("SyncCalendar: want error from loadCalendarClient, got nil")
	}

	calRow, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if got := storage.NullableToString(calRow.LastSyncError); got == "" {
		t.Fatal("LastSyncError empty: an unsyncable calendar still shows healthy")
	}
	if got := storage.NullableToString(calRow.LastSyncAttemptedAt); got == "" {
		t.Fatal("LastSyncAttemptedAt empty: the failed sync attempt was not recorded")
	}
}

// TestEngineSyncCalendarRecordsHealthAfterContextExpiry is the regression
// test for the stale health bookkeeping on a timed-out sync. SyncAll gives
// each calendar a deadline. When the context expires mid-sync, the deferred
// updateSyncHealth ran with the expired context. The write failed, so
// LastSyncError stayed empty and the ambient ⚠ glyph never lit up for the
// timed-out calendar. The health write must ignore the expiry.
func TestEngineSyncCalendarRecordsHealthAfterContextExpiry(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	syncCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The server cancels the sync context on the first request it sees.
	// That imitates the per-calendar deadline hitting mid-sync, after
	// loadCalendarClient already succeeded.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		http.Error(w, "deadline exceeded", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	remoteAccount, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "deadline", ServerUrl: server.URL, AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	remoteURL := server.URL + "/calendar"
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: calendarID, AccountID: &remoteAccount.ID, RemoteUrl: &remoteURL,
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	engine.credStore.(*mockCredStore).creds[remoteAccount.ID] = auth.Credential{
		AccountID: remoteAccount.ID, Username: "user", Password: "secret",
	}

	result, err := engine.SyncCalendar(syncCtx, calendarID, ConflictServerWins)
	if err != nil {
		t.Fatalf("SyncCalendar: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("sync errors empty: the canceled sync must report failures")
	}
	for _, syncErr := range result.Errors {
		if strings.Contains(syncErr.Error(), "update sync health") {
			t.Fatalf("sync errors = %v, want no health-write failure", result.Errors)
		}
	}

	calRow, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if got := storage.NullableToString(calRow.LastSyncError); got == "" {
		t.Fatal("LastSyncError empty: the timed-out sync still shows healthy")
	}
	if got := storage.NullableToString(calRow.LastSyncAttemptedAt); got == "" {
		t.Fatal("LastSyncAttemptedAt empty: the failed attempt was not recorded")
	}
}

// TestEnginePushSerializesConcurrentNewResourceCreate is the regression test
// for issue #225. Two concurrent push runs for the same calendar (e.g. an
// opportunistic save-time PushLocalEdits racing a periodic SyncCalendar) must not
// both create a server object for the same never-pushed, etag-less resource.
// Before the per-calendar push lock, each run read the same dirty
// sync_resource (RemoteUrl=""). Each minted a distinct random href. Each PUT
// it with no If-Match precondition. The server then had two objects for one UID.
//
// The two runs use distinct Engine instances over a shared DB. That matches the
// TUI, which builds a fresh sync.Service per operation (see newSyncService).
// An Engine-scoped lock would not catch this. The lock registry is keyed by DB.
func TestEnginePushSerializesConcurrentNewResourceCreate(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	// A plain :memory: database hands each pooled connection its own private
	// DB; pin the pool to one connection so both goroutines share state.
	db.SetMaxOpenConns(1)
	ctx := context.Background()

	// A second Engine over the same DB, standing in for the separate
	// sync.Service the TUI spins up for the racing operation.
	engine2 := NewEngine(db, q, &mockCredStore{creds: make(map[int64]auth.Credential)},
		calendar.NewService(db, q), event.NewService(db, q),
		todo.NewService(db, q), journal.NewService(db, q), nil)
	engines := [2]*Engine{engine, engine2}

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "new-resource")

	// A brand-new dirty resource: never pushed (RemoteUrl="") and no ETag.
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "new-resource",
		OwnerType:    "event",
		RemoteUrl:    "",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource new-resource: %v", err)
	}

	var mu gosync.Mutex
	creates := 0
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			return newResponse(http.StatusInternalServerError, nil), nil
		}
		if got := r.Header.Get("If-Match"); got != "" {
			t.Errorf("If-Match = %q, want empty for a first-time create", got)
		}
		mu.Lock()
		creates++
		mu.Unlock()
		// Widen the race window so an unserialized second run reads the dirty
		// row before this run clears it.
		time.Sleep(50 * time.Millisecond)
		return newResponse(http.StatusCreated, map[string]string{"ETag": `"etag-new"`}), nil
	})

	var wg gosync.WaitGroup
	for i := range engines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := engines[i].push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins, false); err != nil {
				t.Errorf("push: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if creates != 1 {
		t.Fatalf("server create PUTs = %d, want 1 (concurrent pushes created duplicate objects)", creates)
	}
}

func TestEnginePushRecordsConflictOnPreconditionFailure(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "conflict-event")

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			if r.URL.Path != "/calendar/conflict-event.ics" {
				t.Fatalf("PUT path = %s, want /calendar/conflict-event.ics", r.URL.Path)
			}
			if got := r.Header.Get("If-Match"); got != `"etag-before"` {
				t.Fatalf("If-Match = %q, want %q", got, `"etag-before"`)
			}
			return &http.Response{
				StatusCode: http.StatusPreconditionFailed,
				Status:     "412 Precondition Failed",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("precondition failed")),
				Request:    r,
			}, nil
		case http.MethodGet:
			if r.URL.Path != "/calendar/conflict-event.ics" {
				t.Fatalf("GET path = %s, want /calendar/conflict-event.ics", r.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header: http.Header{
					"Content-Type": []string{"text/calendar; charset=utf-8"},
					"Etag":         []string{`"etag-server"`},
				},
				Body: io.NopCloser(strings.NewReader(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:conflict-event
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Server version
END:VEVENT
END:VCALENDAR
`)),
				Request: r,
			}, nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "conflict-event",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/conflict-event.ics",
		Etag:         `"etag-before"`,
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource conflict-event: %v", err)
	}

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictPrompt, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.pushed != 0 {
		t.Fatalf("pushed = %d, want 0", result.pushed)
	}
	if result.conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", result.conflicts)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %d, want 0", len(result.errors))
	}

	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("sync conflicts = %d, want 1", len(conflicts))
	}
	if conflicts[0].Uid != "conflict-event" {
		t.Fatalf("conflict uid = %q, want conflict-event", conflicts[0].Uid)
	}
	if conflicts[0].ServerEtag != "etag-server" {
		t.Fatalf("ServerEtag = %q, want %q", conflicts[0].ServerEtag, "etag-server")
	}
	// The recorded local body must be the exact iCal we attempted to PUT.
	// The push path exports the resource once before the PUT and reuses that
	// result for the conflict record instead of re-exporting (issue #264), so
	// it must still match a fresh export of the same local resource.
	wantLocal, err := engine.exportResource(ctx, "event", "conflict-event")
	if err != nil {
		t.Fatalf("exportResource: %v", err)
	}
	if conflicts[0].LocalIcal != string(wantLocal) {
		t.Fatalf("LocalIcal = %q, want %q", conflicts[0].LocalIcal, string(wantLocal))
	}
	if !strings.Contains(conflicts[0].LocalIcal, "SUMMARY:Test conflict-event") {
		t.Fatalf("LocalIcal missing local summary, got %q", conflicts[0].LocalIcal)
	}
	var evtID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM events WHERE uid = ? AND recurrence_id = ''`, "conflict-event").Scan(&evtID); err != nil {
		t.Fatalf("lookup event id: %v", err)
	}
	if conflicts[0].OwnerID != evtID {
		t.Fatalf("OwnerID = %d, want %d", conflicts[0].OwnerID, evtID)
	}

	dirty, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(dirty) != 1 {
		t.Fatalf("dirty resources = %d, want 1", len(dirty))
	}
	if dirty[0].Uid != "conflict-event" {
		t.Fatalf("remaining dirty uid = %q, want conflict-event", dirty[0].Uid)
	}

	// Mark the row resolved, then push the same dirty resource again. The
	// resolved row no longer blocks the push. The fresh 412 must reopen the
	// row (upsert resets resolved_at/resolution) instead of stranding it as
	// resolved while the local edit still has nowhere to go.
	if err := engine.markConflictResolved(ctx, calendarID, "conflict-event", ResolutionServerAuto); err != nil {
		t.Fatalf("markConflictResolved: %v", err)
	}
	second, err := engine.push(ctx, client, calendarID, "", "", ConflictPrompt, false)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if second.conflicts != 1 {
		t.Fatalf("second push conflicts = %d, want 1 (re-detected conflict)", second.conflicts)
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar after reopen: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open conflicts after reopen = %d, want 1 (one row, reopened)", len(open))
	}
	if open[0].ResolvedAt != nil || open[0].Resolution != nil {
		t.Fatalf("reopened row still carries resolution = (%v, %v), want (nil, nil)", open[0].ResolvedAt, open[0].Resolution)
	}
}

// TestEnginePushLostPutResponseIsNotFalseConflict reproduces issue #294: the
// first PUT reaches the server and mutates the resource, but the response is
// lost (a transient "connection reset"). Retry re-issues the PUT with the
// stale pre-PUT If-Match, which the server now 412s because its ETag already
// advanced. Without idempotency-aware recovery this surfaces as a spurious
// conflict even though our write actually won. The push must instead detect
// that the server holds exactly our payload and finalize it as a success.
func TestEnginePushLostPutResponseIsNotFalseConflict(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "lost-put")

	var putBody []byte
	putCount := 0
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			putBody = body
			putCount++
			if putCount == 1 {
				// The PUT landed server-side, but the response is lost on the
				// wire while reading it back. Classified transient.
				return nil, fmt.Errorf("read response: connection reset by peer")
			}
			// The retried conditional PUT carries the stale If-Match, so the
			// server (whose ETag already advanced) rejects it.
			return &http.Response{
				StatusCode: http.StatusPreconditionFailed,
				Status:     "412 Precondition Failed",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("precondition failed")),
				Request:    r,
			}, nil
		case http.MethodGet:
			// The server now holds exactly the body we PUT on the first try.
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header: http.Header{
					"Content-Type": []string{"text/calendar; charset=utf-8"},
					"Etag":         []string{`"etag-after"`},
				},
				Body:    io.NopCloser(bytes.NewReader(putBody)),
				Request: r,
			}, nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "lost-put",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/lost-put.ics",
		Etag:         `"etag-before"`,
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource lost-put: %v", err)
	}

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictPrompt, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0 (lost response is not a real conflict)", result.conflicts)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %v, want none", result.errors)
	}
	if result.pushed != 1 {
		t.Fatalf("pushed = %d, want 1 (our write actually landed)", result.pushed)
	}

	// No spurious conflict row recorded.
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("sync conflicts = %d, want 0", len(conflicts))
	}

	// The resource is finalized with the server's advanced ETag and no longer dirty.
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "lost-put"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Etag != "etag-after" {
		t.Fatalf("Etag = %q, want %q (adopted from the landed write)", res.Etag, "etag-after")
	}
	if res.Dirty != 0 {
		t.Fatalf("Dirty = %d, want 0 (write succeeded)", res.Dirty)
	}
}
