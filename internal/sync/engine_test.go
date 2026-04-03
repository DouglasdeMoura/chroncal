package sync

import (
	"context"
	"database/sql"
	"errors"
	"io"
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

	_, err := db.Exec(
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

	result, err := engine.processTombstones(ctx, client, calendarID)
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

	tombstoneResult, err := engine.processTombstones(ctx, client, calendarID)
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
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

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
			if r.URL.Path != "/calendar/normalized-new.ics" {
				t.Fatalf("PUT path = %s, want /calendar/normalized-new.ics", r.URL.Path)
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
    <d:href>/calendar/normalized-new.ics</d:href>
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
	if res.RemoteUrl != "/calendar/normalized-new.ics" {
		t.Fatalf("RemoteUrl = %q, want /calendar/normalized-new.ics", res.RemoteUrl)
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
	if res.RemoteUrl != "/calendar/normalized-new.ics" {
		t.Fatalf("RemoteUrl after pull = %q, want /calendar/normalized-new.ics", res.RemoteUrl)
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
