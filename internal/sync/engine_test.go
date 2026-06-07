package sync

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

type testHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (c testHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.do(req)
}

func newTestEngine(t *testing.T) (*Engine, *sql.DB, *storage.Queries) {
	t.Helper()

	db, q := testutil.NewTestDB(t)
	credStore := &mockCredStore{creds: make(map[int64]auth.Credential)}
	calendars := calendar.NewService(db, q)
	events := event.NewService(db, q)
	todos := todo.NewService(db, q)
	journals := journal.NewService(db, q)
	return NewEngine(db, q, credStore, calendars, events, todos, journals, nil), db, q
}

func newTestCalDAVClient(t *testing.T, do func(*http.Request) (*http.Response, error)) *caldav.Client {
	t.Helper()

	client, err := caldav.NewClient(testHTTPClient{do: do}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func overrideRemoteObjectNameGenerator(t *testing.T, name string) {
	t.Helper()

	prev := newRemoteObjectName
	newRemoteObjectName = func() string { return name }
	t.Cleanup(func() {
		newRemoteObjectName = prev
	})
}

func newResponse(statusCode int, headers map[string]string) *http.Response {
	header := make(http.Header, len(headers))
	for key, value := range headers {
		header.Set(key, value)
	}
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     header,
		Body:       io.NopCloser(http.NoBody),
	}
}

func insertTestEvent(t *testing.T, db *sql.DB, calendarID int64, uid string) {
	t.Helper()

	_, err := db.ExecContext(t.Context(),
		"INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		uid,
		calendarID,
		"Test "+uid,
		"2026-04-03T10:00:00Z",
		"2026-04-03T11:00:00Z",
		"CONFIRMED",
		"OPAQUE",
		"PUBLIC",
	)
	if err != nil {
		t.Fatalf("insert event %q: %v", uid, err)
	}
}

func TestEnginePushContinuesAfterResourceFailure(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "push-fail")
	insertTestEvent(t, db, calendarID, "push-success")

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/calendar/push-fail.ics":
			return newResponse(http.StatusServiceUnavailable, nil), nil
		case "/calendar/push-success.ics":
			return newResponse(http.StatusCreated, map[string]string{"ETag": `"etag-success"`}), nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "push-fail",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/push-fail.ics",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource push-fail: %v", err)
	}
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "push-success",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/push-success.ics",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource push-success: %v", err)
	}

	result, err := engine.push(ctx, client, calendarID, "", ConflictServerWins)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.pushed != 1 {
		t.Fatalf("pushed = %d, want 1", result.pushed)
	}
	if len(result.errors) != 1 {
		t.Fatalf("errors = %d, want 1", len(result.errors))
	}

	dirty, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(dirty) != 1 {
		t.Fatalf("dirty resources = %d, want 1", len(dirty))
	}
	if dirty[0].Uid != "push-fail" {
		t.Fatalf("remaining dirty uid = %q, want push-fail", dirty[0].Uid)
	}
}

// TestEnginePushSkipsForeignOrganizedEvents confirms that push refuses to
// PUT meetings the calendar owner did not organize. CalDAV servers reject
// attendee PUTs (Google returns HTTP 400 with a vague <D:error/>) so
// retrying every sync is just dead weight — we clear the dirty flag and
// leave the local row alone.
func TestEnginePushSkipsForeignOrganizedEvents(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	if err := q.UpdateCalendarOwnerEmail(ctx, storage.UpdateCalendarOwnerEmailParams{
		ID:         calendarID,
		OwnerEmail: "me@example.com",
	}); err != nil {
		t.Fatalf("UpdateCalendarOwnerEmail: %v", err)
	}

	insertTestEvent(t, db, calendarID, "foreign-event")
	insertTestEvent(t, db, calendarID, "owned-event")

	var foreignID, ownedID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM events WHERE uid='foreign-event'`).Scan(&foreignID); err != nil {
		t.Fatalf("lookup foreign id: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT id FROM events WHERE uid='owned-event'`).Scan(&ownedID); err != nil {
		t.Fatalf("lookup owned id: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO event_attendees (event_id, email, role, organizer) VALUES (?, ?, 'CHAIR', 1), (?, ?, 'REQ-PARTICIPANT', 0)`,
		foreignID, "stranger@example.com",
		foreignID, "me@example.com",
	); err != nil {
		t.Fatalf("insert foreign attendees: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO event_attendees (event_id, email, role, organizer) VALUES (?, ?, 'CHAIR', 1)`,
		ownedID, "ME@example.com",
	); err != nil {
		t.Fatalf("insert owned attendees: %v", err)
	}

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "foreign-event", OwnerType: "event",
		RemoteUrl: "/calendar/foreign-event.ics", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource foreign: %v", err)
	}
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "owned-event", OwnerType: "event",
		RemoteUrl: "/calendar/owned-event.ics", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource owned: %v", err)
	}

	var puttedPaths []string
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		puttedPaths = append(puttedPaths, r.URL.Path)
		return newResponse(http.StatusCreated, map[string]string{"ETag": `"new-etag"`}), nil
	})

	result, err := engine.push(ctx, client, calendarID, "/calendar/", ConflictServerWins)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %d, want 0: %v", len(result.errors), result.errors)
	}
	if len(puttedPaths) != 1 || puttedPaths[0] != "/calendar/owned-event.ics" {
		t.Fatalf("PUT paths = %v, want only /calendar/owned-event.ics", puttedPaths)
	}

	dirty, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(dirty) != 0 {
		t.Fatalf("dirty after push = %d, want 0 (foreign should be cleared, owned should be PUT)", len(dirty))
	}
}

// TestEnginePushClearsDirtyWhenLocalRowMissing verifies that a dirty
// sync_resource pointing at a UID with no live event row stops retrying.
// This unblocks zombie rows left over from inconsistent state (e.g. user
// purged the local event but the sync_resource survived) instead of
// emitting "get event by uid" errors on every sync run.
func TestEnginePushClearsDirtyWhenLocalRowMissing(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "ghost-uid", OwnerType: "event",
		RemoteUrl: "/calendar/ghost.ics", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected HTTP %s %s — push should not have hit the wire", r.Method, r.URL.Path)
		return nil, nil
	})

	result, err := engine.push(ctx, client, calendarID, "/calendar/", ConflictServerWins)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %d, want 0: %v", len(result.errors), result.errors)
	}

	dirty, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(dirty) != 0 {
		t.Fatalf("dirty after push = %d, want 0", len(dirty))
	}
}

// TestEngineExportResourceFallsBackToOrphanOverride covers Google's
// `<master>_R<rid>@google.com` orphan-instance pattern: the iCal stream
// gives an isolated occurrence with a synthetic suffixed UID and a
// RECURRENCE-ID, so we import an override row but never receive a master.
// The exporter must still emit something pushable instead of erroring.
func TestEngineExportResourceFallsBackToOrphanOverride(t *testing.T) {
	t.Parallel()

	engine, db, _ := newTestEngine(t)
	ctx := context.Background()

	const uid = "abc_R20250609T190000@google.com"
	if _, err := db.ExecContext(ctx,
		"INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class, recurrence_id) VALUES (?, 1, ?, ?, ?, 'CONFIRMED', 'OPAQUE', 'PUBLIC', ?)",
		uid, "Orphan instance",
		"2025-06-09T19:00:00Z", "2025-06-09T20:00:00Z",
		"2025-06-09T19:00:00Z",
	); err != nil {
		t.Fatalf("insert orphan override: %v", err)
	}

	data, err := engine.exportResource(ctx, "event", uid)
	if err != nil {
		t.Fatalf("exportResource: %v", err)
	}
	if !strings.Contains(string(data), "UID:"+uid) {
		t.Fatalf("export missing UID:\n%s", string(data))
	}
	if !strings.Contains(string(data), "RECURRENCE-ID") {
		t.Fatalf("export missing RECURRENCE-ID:\n%s", string(data))
	}
}

// TestEnginePullClearsDirtyAfterImport prevents the regression where pull's
// persistImported call flipped dirty=1 (via the event service's Replace*
// methods which mark the sync_resource dirty as a side effect for user
// edits) and UpsertSyncResource's `dirty = MAX(...)` clause preserved that
// 1, so every sync re-dirtied resources it had just imported and the next
// push round-tripped them back to the server. The engine must explicitly
// clear dirty after a sync-driven import so the resource lands clean.
func TestEnginePullClearsDirtyAfterImport(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const responseBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/post-import.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-fresh&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/abc</d:sync-token>
</d:multistatus>`

	const fetchBody = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:post-import-uid
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Post-import event
ATTENDEE;CN=Other;ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:other@example.com
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
    <d:href>/calendar/post-import.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-fresh&quot;</d:getetag>
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

	if _, err := engine.pull(ctx, client, calendarID, "/calendar/"); err != nil {
		t.Fatalf("pull: %v", err)
	}

	dirty, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(dirty) != 0 {
		t.Fatalf("dirty after pull = %d, want 0 (sync-imports must land clean)", len(dirty))
	}
}

// TestEnginePullToleratesMultigetMissingPath verifies that a per-resource
// 404 returned by calendar-multiget after sync-collection nominated the path
// no longer aborts the whole pull. Surviving resources still import; missing
// paths are NOT soft-deleted (a 404 here can be a transient server quirk,
// not a real deletion — we lost real user data the one time we tried that);
// and the sync-token is held back so the next sync re-lists the same change
// set and gets another chance to fetch the missing bodies.
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

	result, err := engine.push(ctx, client, calendarID, "", ConflictPrompt)
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
}

func TestEnginePushServerWinsAdoptsServerVersion(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "server-wins-event")

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
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
UID:server-wins-event
DTSTAMP:20260403T120000Z
DTSTART:20260403T130000Z
DTEND:20260403T140000Z
SUMMARY:Server Wins Version
DESCRIPTION:server wins update
STATUS:CONFIRMED
TRANSP:OPAQUE
SEQUENCE:2
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
		Uid:          "server-wins-event",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/server-wins-event.ics",
		Etag:         "etag-before",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	result, err := engine.push(ctx, client, calendarID, "", ConflictServerWins)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", result.conflicts)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %d, want 0", len(result.errors))
	}

	evt, err := q.GetEventByUID(ctx, "server-wins-event")
	if err != nil {
		t.Fatalf("GetEventByUID: %v", err)
	}
	if evt.Title != "Server Wins Version" {
		t.Fatalf("Title = %q, want Server Wins Version", evt.Title)
	}
	if storage.NullableToString(evt.Description) != "server wins update" {
		t.Fatalf("Description = %q, want server wins update", storage.NullableToString(evt.Description))
	}
	if evt.StartTime != "2026-04-03T13:00:00Z" {
		t.Fatalf("StartTime = %q, want 2026-04-03T13:00:00Z", evt.StartTime)
	}
	if evt.EndTime != "2026-04-03T14:00:00Z" {
		t.Fatalf("EndTime = %q, want 2026-04-03T14:00:00Z", evt.EndTime)
	}
	if evt.Sequence != 2 {
		t.Fatalf("Sequence = %d, want 2", evt.Sequence)
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        "server-wins-event",
	})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 0 {
		t.Fatalf("Dirty = %d, want 0", res.Dirty)
	}
	if res.Etag != "etag-server" {
		t.Fatalf("Etag = %q, want etag-server", res.Etag)
	}

	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("sync conflicts = %d, want 0", len(conflicts))
	}
}

func TestEngineProcessTombstonesContinuesAfterDeleteFailure(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/calendar/delete-fail.ics":
			return newResponse(http.StatusServiceUnavailable, nil), nil
		case "/calendar/delete-success.ics":
			return newResponse(http.StatusNoContent, nil), nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID,
		Uid:        "delete-fail",
		RemoteUrl:  "/calendar/delete-fail.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone delete-fail: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID,
		Uid:        "delete-success",
		RemoteUrl:  "/calendar/delete-success.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone delete-success: %v", err)
	}

	result, err := engine.processTombstones(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("processTombstones: %v", err)
	}
	if result.deleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.deleted)
	}

	tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListTombstonesByCalendar: %v", err)
	}
	if len(tombstones) != 1 {
		t.Fatalf("remaining tombstones = %d, want 1", len(tombstones))
	}
	if tombstones[0].Uid != "delete-fail" {
		t.Fatalf("remaining tombstone uid = %q, want delete-fail", tombstones[0].Uid)
	}
}

func TestEnginePullSkipsTombstonedRemoteResource(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "tombstoned-event",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/tombstoned.ics",
		Etag:         `"etag-remote"`,
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID,
		Uid:        "tombstoned-event",
		RemoteUrl:  "/calendar/tombstoned.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}

	remoteExists := true
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case "REPORT":
			if r.URL.Path != "/calendar/" {
				t.Fatalf("REPORT path = %s, want /calendar/", r.URL.Path)
			}
			body := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"></d:multistatus>`
			if remoteExists {
				body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/tombstoned.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-remote&quot;</d:getetag>
        <cal:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:tombstoned-event
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Tombstoned event
END:VEVENT
END:VCALENDAR
</cal:calendar-data>
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
		case http.MethodDelete:
			if r.URL.Path != "/calendar/tombstoned.ics" {
				t.Fatalf("DELETE path = %s, want /calendar/tombstoned.ics", r.URL.Path)
			}
			remoteExists = false
			return newResponse(http.StatusNoContent, nil), nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	pullResult, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pullResult.pulled != 0 {
		t.Fatalf("pulled = %d, want 0", pullResult.pulled)
	}

	if _, err := q.GetEventByUID(ctx, "tombstoned-event"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetEventByUID err = %v, want sql.ErrNoRows", err)
	}

	tombstoneResult, err := engine.processTombstones(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("processTombstones: %v", err)
	}
	if tombstoneResult.deleted != 1 {
		t.Fatalf("deleted = %d, want 1", tombstoneResult.deleted)
	}
	if len(tombstoneResult.errors) != 0 {
		t.Fatalf("errors = %d, want 0", len(tombstoneResult.errors))
	}

	tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListTombstonesByCalendar: %v", err)
	}
	if len(tombstones) != 0 {
		t.Fatalf("remaining tombstones = %d, want 0", len(tombstones))
	}

	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        "tombstoned-event",
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSyncResource err = %v, want sql.ErrNoRows", err)
	}
}

func TestEnginePushNormalizesNewResourcePath(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	overrideRemoteObjectNameGenerator(t, "opaque-resource.ics")

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "normalized-new")

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "normalized-new",
		OwnerType:    "event",
		RemoteUrl:    "",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			if r.URL.Path != "/calendar/opaque-resource.ics" {
				t.Fatalf("PUT path = %s, want /calendar/opaque-resource.ics", r.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusCreated,
				Status:     "201 Created",
				Header:     http.Header{"Etag": []string{`"etag-new"`}},
				Body:       io.NopCloser(http.NoBody),
				Request:    r,
			}, nil
		case "REPORT":
			if r.URL.Path != "/calendar/" {
				t.Fatalf("REPORT path = %s, want /calendar/", r.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Status:     "207 Multi-Status",
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/opaque-resource.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-new&quot;</d:getetag>
        <cal:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:normalized-new
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Normalized path
END:VEVENT
END:VCALENDAR
</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
				Request: r,
			}, nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	pushResult, err := engine.push(ctx, client, calendarID, "/calendar/", ConflictServerWins)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushResult.pushed != 1 {
		t.Fatalf("pushed = %d, want 1", pushResult.pushed)
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        "normalized-new",
	})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.RemoteUrl != "/calendar/opaque-resource.ics" {
		t.Fatalf("RemoteUrl = %q, want /calendar/opaque-resource.ics", res.RemoteUrl)
	}

	pullResult, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pullResult.pulled != 0 {
		t.Fatalf("pulled = %d, want 0", pullResult.pulled)
	}
	if pullResult.deleted != 0 {
		t.Fatalf("deleted = %d, want 0", pullResult.deleted)
	}

	res, err = q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        "normalized-new",
	})
	if err != nil {
		t.Fatalf("GetSyncResource after pull: %v", err)
	}
	if res.RemoteUrl != "/calendar/opaque-resource.ics" {
		t.Fatalf("RemoteUrl after pull = %q, want /calendar/opaque-resource.ics", res.RemoteUrl)
	}
}

func TestEnginePushIgnoresUIDWhenAssigningNewResourcePath(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	overrideRemoteObjectNameGenerator(t, "opaque-malicious.ics")

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "../../escape")

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "../../escape",
		OwnerType:    "event",
		RemoteUrl:    "",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Path != "/calendar/opaque-malicious.ics" {
			t.Fatalf("PUT path = %s, want /calendar/opaque-malicious.ics", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Status:     "201 Created",
			Header:     http.Header{"Etag": []string{`"etag-malicious"`}},
			Body:       io.NopCloser(http.NoBody),
			Request:    r,
		}, nil
	})

	pushResult, err := engine.push(ctx, client, calendarID, "/calendar/", ConflictServerWins)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushResult.pushed != 1 {
		t.Fatalf("pushed = %d, want 1", pushResult.pushed)
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        "../../escape",
	})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.RemoteUrl != "/calendar/opaque-malicious.ics" {
		t.Fatalf("RemoteUrl = %q, want /calendar/opaque-malicious.ics", res.RemoteUrl)
	}
}

func TestEnginePushRejectsOffOriginStoredRemoteURL(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "off-origin-push")

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "off-origin-push",
		OwnerType:    "event",
		RemoteUrl:    "https://attacker.example/calendar/off-origin-push.ics",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	requests := 0
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		requests++
		return newResponse(http.StatusCreated, map[string]string{"ETag": `"etag-off-origin"`}), nil
	})

	result, err := engine.push(ctx, client, calendarID, "/calendar/", ConflictServerWins)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.pushed != 0 {
		t.Fatalf("pushed = %d, want 0", result.pushed)
	}
	if len(result.errors) != 1 {
		t.Fatalf("errors = %d, want 1", len(result.errors))
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestEngineProcessTombstonesRejectsOffOriginRemoteURL(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID,
		Uid:        "off-origin-tombstone",
		RemoteUrl:  "https://attacker.example/calendar/off-origin-tombstone.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}

	requests := 0
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		requests++
		return newResponse(http.StatusNoContent, nil), nil
	})

	result, err := engine.processTombstones(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("processTombstones: %v", err)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0", result.deleted)
	}
	if len(result.errors) != 1 {
		t.Fatalf("errors = %d, want 1", len(result.errors))
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestEnginePullDeletesLocalResourceWhenServerRemovesIt(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "remote-deleted")

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "remote-deleted",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/remote-deleted.ics",
		Etag:         "etag-remote",
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "REPORT" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"></d:multistatus>`)),
			Request: r,
		}, nil
	})

	pullResult, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pullResult.deleted != 1 {
		t.Fatalf("deleted = %d, want 1", pullResult.deleted)
	}
	if pullResult.pulled != 0 {
		t.Fatalf("pulled = %d, want 0", pullResult.pulled)
	}

	if _, err := q.GetEventByUID(ctx, "remote-deleted"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetEventByUID err = %v, want sql.ErrNoRows", err)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        "remote-deleted",
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSyncResource err = %v, want sql.ErrNoRows", err)
	}
}

// GMX (and other Cosmo-derived CalDAV servers) rewrite object hrefs on the
// server side — a resource PUT at /cal/<user>/... is later reported under
// /cal/<uuid>/... in REPORT responses. Pull must recognise the resource by
// UID and avoid treating the path change as a remote deletion.
func TestEnginePullPreservesLocalWhenServerRewritesHref(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "rewritten")

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "rewritten",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/user@example.com/rewritten.ics",
		Etag:         "etag-before-rewrite",
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "REPORT" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/00000000-0000-0000-0000-aaaaaaaaaaaa/rewritten.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-after-rewrite&quot;</d:getetag>
        <cal:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:rewritten
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Rewritten by server
END:VEVENT
END:VCALENDAR
</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
			Request: r,
		}, nil
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/user@example.com/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (server rewrote path, not a deletion)", result.deleted)
	}

	if _, err := q.GetEventByUID(ctx, "rewritten"); err != nil {
		t.Fatalf("GetEventByUID err = %v, event was unexpectedly deleted", err)
	}

	resources, err := q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncResourcesByCalendar: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("sync_resources len = %d, want 1", len(resources))
	}
	if resources[0].Uid != "rewritten" {
		t.Fatalf("uid = %q, want %q", resources[0].Uid, "rewritten")
	}
	if !strings.Contains(resources[0].RemoteUrl, "00000000-0000-0000-0000-aaaaaaaaaaaa") {
		t.Fatalf("RemoteUrl = %q, expected it to track the new server path", resources[0].RemoteUrl)
	}
}

func TestEngineSyncCalendarMetadataPushesLocalColor(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        1,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://example.com/cal/work"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE calendars
		SET color = '#112233', remote_color = '#445566', color_dirty = 1
		WHERE id = 1
	`); err != nil {
		t.Fatalf("seed calendar color state: %v", err)
	}

	sawPropPatch := false
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case "PROPFIND":
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/cal/work</d:href>
    <d:propstat>
      <d:prop><ic:calendar-color>#445566</ic:calendar-color></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
			}, nil
		case "PROPPATCH":
			sawPropPatch = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !strings.Contains(string(body), "#112233") {
				t.Fatalf("PROPPATCH body = %s", string(body))
			}
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response><d:href>/cal/work</d:href><d:propstat><d:prop /><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`)),
			}, nil
		default:
			t.Fatalf("unexpected method %s", r.Method)
			return nil, nil
		}
	})

	if err := engine.syncCalendarMetadata(ctx, client, 1, "https://example.com/cal/work"); err != nil {
		t.Fatalf("syncCalendarMetadata: %v", err)
	}
	if !sawPropPatch {
		t.Fatal("expected color push PROPPATCH")
	}

	cal, err := q.GetCalendar(ctx, 1)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if got := storage.NullableToString(cal.RemoteColor); got != "#112233" {
		t.Fatalf("RemoteColor = %q, want #112233", got)
	}
	if cal.ColorDirty != 0 {
		t.Fatalf("ColorDirty = %d, want 0", cal.ColorDirty)
	}
}

func TestEngineSyncCalendarMetadataAdoptsRemoteColor(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        1,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://example.com/cal/work"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE calendars
		SET color = '#445566', remote_color = '#445566', color_dirty = 0
		WHERE id = 1
	`); err != nil {
		t.Fatalf("seed calendar color state: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "PROPFIND" {
			t.Fatalf("unexpected method %s", r.Method)
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/cal/work</d:href>
    <d:propstat>
      <d:prop><ic:calendar-color>#778899</ic:calendar-color></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
		}, nil
	})

	if err := engine.syncCalendarMetadata(ctx, client, 1, "https://example.com/cal/work"); err != nil {
		t.Fatalf("syncCalendarMetadata: %v", err)
	}

	cal, err := q.GetCalendar(ctx, 1)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Color != "#778899" {
		t.Fatalf("Color = %q, want #778899", cal.Color)
	}
	if got := storage.NullableToString(cal.RemoteColor); got != "#778899" {
		t.Fatalf("RemoteColor = %q, want #778899", got)
	}
	if cal.ColorDirty != 0 {
		t.Fatalf("ColorDirty = %d, want 0", cal.ColorDirty)
	}
}

// TestEnginePullPaginatesTruncatedSyncCollection reproduces the Google
// initial-snapshot data loss: the server truncates the sync-collection
// response (RFC 6578 §3.6 — a 507 marker on the collection plus a
// continuation token). The engine must page until complete and diff local
// state against the UNION of pages. Before the fix, every local UID beyond
// page one was soft-deleted (73 real events on one production calendar).
func TestEnginePullPaginatesTruncatedSyncCollection(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	// "survivor" exists locally and on the server — but only on PAGE TWO of
	// the truncated snapshot. "gone-uid" exists locally and on neither page.
	insertTestEvent(t, db, calendarID, "survivor")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "survivor", OwnerType: "event",
		RemoteUrl: "/calendar/survivor.ics", Etag: "etag-survivor",
		Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource survivor: %v", err)
	}
	insertTestEvent(t, db, calendarID, "gone-uid")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "gone-uid", OwnerType: "event",
		RemoteUrl: "/calendar/gone.ics", Etag: "etag-gone",
		Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource gone: %v", err)
	}

	const pageOne = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
 <D:response>
  <D:href>/calendar/new-a.ics</D:href>
  <D:propstat>
   <D:status>HTTP/1.1 200 OK</D:status>
   <D:prop><D:getetag>&quot;etag-a&quot;</D:getetag></D:prop>
  </D:propstat>
 </D:response>
 <D:response>
  <D:href>/calendar/</D:href>
  <D:status>HTTP/1.1 507 Insufficient Storage</D:status>
 </D:response>
 <D:sync-token>PAGE2-TOKEN</D:sync-token>
</D:multistatus>`

	const pageTwo = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
 <D:response>
  <D:href>/calendar/survivor.ics</D:href>
  <D:propstat>
   <D:status>HTTP/1.1 200 OK</D:status>
   <D:prop><D:getetag>&quot;etag-survivor&quot;</D:getetag></D:prop>
  </D:propstat>
 </D:response>
 <D:sync-token>FINAL-TOKEN</D:sync-token>
</D:multistatus>`

	const newAICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:new-a-uid
DTSTAMP:20260606T120000Z
DTSTART:20260606T120000Z
DTEND:20260606T130000Z
SUMMARY:New A
END:VEVENT
END:VCALENDAR
`

	var reportCalls int
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "REPORT" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		body := string(raw)
		if strings.Contains(body, "calendar-multiget") {
			if !strings.Contains(body, "new-a.ics") {
				t.Fatalf("multiget should only fetch the new resource, got:\n%s", body)
			}
			multigetBody := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/new-a.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-a&quot;</d:getetag>
        <cal:calendar-data>` + newAICS + `</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Status:     "207 Multi-Status",
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body:       io.NopCloser(strings.NewReader(multigetBody)),
				Request:    r,
			}, nil
		}
		// sync-collection REPORTs: page 1 for the empty token, page 2 for
		// the continuation token.
		reportCalls++
		page := pageOne
		if strings.Contains(body, "PAGE2-TOKEN") {
			page = pageTwo
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(page)),
			Request:    r,
		}, nil
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if reportCalls != 2 {
		t.Fatalf("sync-collection REPORTs = %d, want 2 (pagination)", reportCalls)
	}
	if result.pulled != 1 {
		t.Fatalf("pulled = %d, want 1 (new-a)", result.pulled)
	}
	if result.deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (only gone-uid)", result.deleted)
	}

	// The page-two event must survive the initial-snapshot deletion sweep.
	if _, err := q.GetEventByUID(ctx, "survivor"); err != nil {
		t.Fatalf("survivor was deleted by the partial-page sweep: %v", err)
	}
	// The genuinely-absent event must still be removed.
	if _, err := q.GetEventByUID(ctx, "gone-uid"); err == nil {
		t.Fatal("gone-uid should have been soft-deleted")
	}
	// The FINAL page's token is the one stored.
	calRow, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if tok := storage.NullableToString(calRow.SyncToken); tok != "FINAL-TOKEN" {
		t.Fatalf("sync_token = %q, want FINAL-TOKEN", tok)
	}
}

func TestSummarizeSyncError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result *SyncResult
		runErr error
		want   string
	}{
		{"run error wins", &SyncResult{Errors: []error{errors.New("ignored")}}, errors.New("boom"), "boom"},
		{"no errors", &SyncResult{}, nil, ""},
		{"single", &SyncResult{Errors: []error{errors.New("e1")}}, nil, "e1"},
		{"multi", &SyncResult{Errors: []error{errors.New("e1"), errors.New("e2"), errors.New("e3")}}, nil, "e1 (+2 more)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summarizeSyncError(c.result, c.runErr); got != c.want {
				t.Errorf("summarizeSyncError = %q, want %q", got, c.want)
			}
		})
	}
}

// discardLogger returns a logger that drops everything, for pure-function
// tests of the deletion chokepoint.
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func uidSet(rs map[string]string) map[string]bool {
	out := make(map[string]bool, len(rs))
	for uid := range rs {
		out[uid] = true
	}
	return out
}

// TestPendingDeletions_AbsenceGate is the core invariant: absence-inferred
// deletions are withheld unless the inventory is complete. This is the single
// guard that three production data-loss bugs would now hit.
func TestPendingDeletions_AbsenceGate(t *testing.T) {
	t.Parallel()
	locals := []storage.SyncResource{
		{Uid: "a", OwnerType: "event", RemoteUrl: "/a.ics"},
		{Uid: "b", OwnerType: "event", RemoteUrl: "/b.ics"},
		{Uid: "never-pushed", OwnerType: "event", RemoteUrl: ""}, // must never delete
	}
	seen := map[string]bool{"a": true} // server still has "a"; "b" is absent

	t.Run("incomplete inventory withholds all", func(t *testing.T) {
		p := newPendingDeletions(discardLogger())
		p.inferFromAbsence(1, locals, seen, false, "truncated")
		if got := uidSet(p.owner); len(got) != 0 {
			t.Errorf("incomplete inventory must withhold; got %v", got)
		}
	})

	t.Run("complete inventory deletes only the absent, pushed row", func(t *testing.T) {
		p := newPendingDeletions(discardLogger())
		p.inferFromAbsence(1, locals, seen, true, "complete")
		got := uidSet(p.owner)
		if !got["b"] {
			t.Error("absent pushed row b should be marked for deletion")
		}
		if got["a"] {
			t.Error("seen row a must not be deleted")
		}
		if got["never-pushed"] {
			t.Error("never-pushed row (empty remote_url) must never be deleted")
		}
	})
}

// TestPendingDeletions_ExplicitAlwaysDeletes confirms explicit (server-404)
// deletions are sound regardless of completeness, and dedupe with absence.
func TestPendingDeletions_ExplicitAlwaysDeletes(t *testing.T) {
	t.Parallel()
	p := newPendingDeletions(discardLogger())
	p.markExplicit(storage.SyncResource{Uid: "gone", OwnerType: "event"})
	p.markExplicit(storage.SyncResource{Uid: "", OwnerType: "event"}) // empty UID ignored
	// An incomplete inventory must not erase an explicit deletion.
	p.inferFromAbsence(1, []storage.SyncResource{{Uid: "x", OwnerType: "event", RemoteUrl: "/x.ics"}},
		map[string]bool{}, false, "truncated")
	got := uidSet(p.owner)
	if !got["gone"] {
		t.Error("explicit deletion should always be marked")
	}
	if got[""] {
		t.Error("empty UID must be ignored")
	}
	if got["x"] {
		t.Error("absence deletion must stay withheld under incomplete inventory")
	}
}

// TestPendingDeletions_DedupExplicitAndAbsence exercises the dedup branch
// (owner already set) when a UID is both explicitly deleted and absent from a
// COMPLETE inventory — it must appear exactly once, not double-counted.
func TestPendingDeletions_DedupExplicitAndAbsence(t *testing.T) {
	t.Parallel()
	p := newPendingDeletions(discardLogger())
	p.markExplicit(storage.SyncResource{Uid: "dup", OwnerType: "event"})
	p.inferFromAbsence(1,
		[]storage.SyncResource{{Uid: "dup", OwnerType: "event", RemoteUrl: "/dup.ics"}},
		map[string]bool{}, true, "complete")
	if got := uidSet(p.owner); len(got) != 1 || !got["dup"] {
		t.Errorf("dup should be present exactly once, got %v", got)
	}
}

// TestEnginePullMultigetMissWithholdsAbsenceDeletions pins the stricter
// behavior the chokepoint enforces: if even one body 404s on multiget during
// an initial snapshot, the inventory is incomplete, so NO absence-inferred
// deletion runs that round — not just the missed path. A locally-tracked row
// absent from the snapshot must survive until a clean sync confirms it.
func TestEnginePullMultigetMissWithholdsAbsenceDeletions(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	// "absent" is tracked locally but will NOT appear in the snapshot at all
	// (a genuine candidate for absence-deletion). "racey" appears in the
	// change list but 404s on multiget (the incompleteness trigger).
	insertTestEvent(t, db, calendarID, "absent")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "absent", OwnerType: "event",
		RemoteUrl: "/calendar/absent.ics", Etag: "e1", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource absent: %v", err)
	}
	insertTestEvent(t, db, calendarID, "racey")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "racey", OwnerType: "event",
		RemoteUrl: "/calendar/racey.ics", Etag: "old", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource racey: %v", err)
	}

	// Initial snapshot (empty token): lists only "racey" (changed), which
	// then 404s on multiget. "absent" is not listed at all.
	const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/calendar/racey.ics</d:href>
    <d:propstat><d:prop><d:getetag>&quot;new&quot;</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/t1</d:sync-token>
</d:multistatus>`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "calendar-multiget") {
			return &http.Response{StatusCode: http.StatusMultiStatus, Status: "207 Multi-Status",
				Header: http.Header{"Content-Type": []string{"application/xml"}},
				Body:   io.NopCloser(strings.NewReader(syncBody)), Request: r}, nil
		}
		// racey.ics 404s on multiget.
		multigetBody := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response><d:href>/calendar/racey.ics</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>
</d:multistatus>`
		return &http.Response{StatusCode: http.StatusMultiStatus, Status: "207 Multi-Status",
			Header: http.Header{"Content-Type": []string{"application/xml"}},
			Body:   io.NopCloser(strings.NewReader(multigetBody)), Request: r}, nil
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (incomplete inventory must withhold ALL absence deletions)", result.deleted)
	}
	// Both rows must still exist — neither the missed one nor the absent one.
	if _, err := q.GetEventByUID(ctx, "absent"); err != nil {
		t.Errorf("absent row was wrongly deleted against a partial inventory: %v", err)
	}
	if _, err := q.GetEventByUID(ctx, "racey"); err != nil {
		t.Errorf("racey row (multiget miss) was wrongly deleted: %v", err)
	}
	// Token must not advance on an incomplete pull.
	calRow, _ := q.GetCalendar(ctx, calendarID)
	if tok := storage.NullableToString(calRow.SyncToken); tok != "" {
		t.Errorf("sync_token = %q, want empty (held back on incomplete pull)", tok)
	}
}

// TestEnginePullFullSnapshotDeletesAbsent covers the legacy QueryAll fallback
// (servers without RFC 6578 sync-collection, e.g. GMX) now that its deletions
// route through the pendingDeletions chokepoint. A sync-collection REPORT that
// returns "unsupported" makes pull() fall back to pullFullSnapshot; a local
// pushed row absent from the QueryAll result must be deleted, while a
// never-pushed row (empty remote_url) must survive.
func TestEnginePullFullSnapshotDeletesAbsent(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "gone-uid")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "gone-uid", OwnerType: "event",
		RemoteUrl: "/calendar/gone.ics", Etag: "e1", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource gone: %v", err)
	}
	insertTestEvent(t, db, calendarID, "local-only")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "local-only", OwnerType: "event",
		RemoteUrl: "", Etag: "", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource local-only: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		// sync-collection REPORT -> reply 422 so the engine falls back to QueryAll.
		if strings.Contains(body, "sync-collection") {
			return &http.Response{
				StatusCode: http.StatusUnprocessableEntity,
				Status:     "422 Unprocessable Entity",
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body:       io.NopCloser(strings.NewReader(`<?xml version="1.0"?><error xmlns="DAV:"/>`)),
				Request:    r,
			}, nil
		}
		// calendar-query REPORT (QueryAll): return an inventory WITHOUT gone.ics.
		queryBody := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/survivor.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;s1&quot;</d:getetag>
        <cal:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:survivor-uid
DTSTAMP:20260606T120000Z
DTSTART:20260606T120000Z
DTEND:20260606T130000Z
SUMMARY:Survivor
END:VEVENT
END:VCALENDAR
</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(queryBody)),
			Request:    r,
		}, nil
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull (fullsnapshot): %v", err)
	}
	if result.deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (gone-uid absent from QueryAll)", result.deleted)
	}
	if _, err := q.GetEventByUID(ctx, "gone-uid"); err == nil {
		t.Error("gone-uid should be deleted (absent from complete QueryAll inventory)")
	}
	if _, err := q.GetEventByUID(ctx, "local-only"); err != nil {
		t.Errorf("never-pushed local-only row must survive: %v", err)
	}
}
