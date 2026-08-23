package sync

import (
	"context"
	"database/sql"
	"errors"

	"io"

	"net/http"
	"net/http/httptest"

	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

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

	result, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins, false)
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

// A server-reported deletion (a top-level 404 in the sync-collection report)
// whose local apply() fails must NOT advance the sync-token. One example is a
// transient SQLITE_BUSY, simulated here by a drop of the events table.
// Otherwise the orphaned local row survives forever. The server is then behind
// the new token and never re-reports the deletion. There is no retry. The
// token must be withheld. The next sync then re-lists the same 404. apply
// gets another shot.
func TestEnginePullHoldsTokenWhenDeletionApplyFails(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "orphan",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/orphan.ics",
		Etag:         "etag-orphan",
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	cal, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}

	// Drop the events table so SoftDeleteEventsByUID errors, deterministically
	// reproducing a failed apply() (the issue cites a transient SQLITE_BUSY).
	// The calendars and sync_resources tables stay intact, so the token-write
	// path is still reachable — only the deletion fails.
	if _, err := db.ExecContext(ctx, "DROP TABLE events"); err != nil {
		t.Fatalf("DROP TABLE events: %v", err)
	}

	// No multiget is expected: the only change is a deletion.
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected HTTP %s %s", r.Method, r.URL.Path)
		return nil, nil
	})

	syncResult := &caldav.SyncCollectionResult{
		SyncToken: "https://example.com/sync/next",
		Changes: []caldav.SyncChange{
			{Path: "/calendar/orphan.ics", Deleted: true},
		},
	}

	result, err := engine.applySyncCollection(ctx, client, calendarID, "/calendar/", cal, syncResult, false)
	if err != nil {
		t.Fatalf("applySyncCollection: %v", err)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (apply failed)", result.deleted)
	}

	// Token withheld so the next sync re-lists and retries the deletion.
	calRow, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if tok := storage.NullableToString(calRow.SyncToken); tok != "" {
		t.Fatalf("sync_token = %q, want empty (held back on deletion-apply failure)", tok)
	}
}

// GMX (and other Cosmo-derived CalDAV servers) rewrite object hrefs on the
// server side. A resource PUT at /cal/<user>/... is later reported under
// /cal/<uuid>/... in REPORT responses. Pull must recognise the resource by
// UID. It must not treat the path change as a remote deletion.
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

// TestEngineSyncCalendarMetadataSkipsFetchWhenDirty reproduces issue #419. When
// the local color is dirty, syncCalendarMetadata must not waste a PROPFIND to
// fetch the remote color (whose value would be discarded anyway). A failure
// of that fetch must not block the wait color push. The mock server fails any
// PROPFIND with 503. The push must still happen. ColorDirty must clear.
func TestEngineSyncCalendarMetadataSkipsFetchWhenDirty(t *testing.T) {
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
			t.Fatalf("unexpected PROPFIND: remote color must not be fetched when local color is dirty")
			return nil, nil
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

// TestEngineSyncCalendarMetadataIgnoresColorFetchForbidden reproduces
// issue #628. A PROPFIND 403 for calendar-color must not fail metadata
// sync. Event pull can then continue. The local color stays in place.
func TestEngineSyncCalendarMetadataIgnoresColorFetchForbidden(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	seedCalendarColorState(t, db, q, "https://example.com/cal/work", "#9e69af", "#9e69af", 0)

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "PROPFIND" {
			t.Fatalf("unexpected method %s", r.Method)
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("nope")),
			Request:    r,
		}, nil
	})

	if err := engine.syncCalendarMetadata(context.Background(), client, 1, "https://example.com/cal/work"); err != nil {
		t.Fatalf("syncCalendarMetadata: %v", err)
	}

	cal, err := q.GetCalendar(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Color != "#9e69af" {
		t.Fatalf("Color = %q, want #9e69af", cal.Color)
	}
	if got := storage.NullableToString(cal.RemoteColor); got != "#9e69af" {
		t.Fatalf("RemoteColor = %q, want #9e69af", got)
	}
}

// TestEngineSyncCalendarMetadataSkipsGoogleColorFetch reproduces
// issue #628. Google colors come from CalendarList. Apple calendar-color
// traffic must not run against CalDAV when there is nothing to push.
func TestEngineSyncCalendarMetadataSkipsGoogleColorFetch(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	seedCalendarColorState(t, db, q,
		"https://apidata.googleusercontent.com/caldav/v2/me@example.com/events",
		"#9e69af", "#9e69af", 0)

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		return nil, nil
	})

	if err := engine.syncCalendarMetadata(context.Background(), client, 1,
		"https://apidata.googleusercontent.com/caldav/v2/me@example.com/events"); err != nil {
		t.Fatalf("syncCalendarMetadata: %v", err)
	}

	cal, err := q.GetCalendar(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Color != "#9e69af" {
		t.Fatalf("Color = %q, want #9e69af", cal.Color)
	}
	if cal.ColorDirty != 0 {
		t.Fatalf("ColorDirty = %d, want 0", cal.ColorDirty)
	}
}

// TestEngineSyncCalendarMetadataPushesGoogleColorViaCalendarList writes a
// dirty Google color through CalendarList, not Apple calendar-color.
func TestEngineSyncCalendarMetadataPushesGoogleColorViaCalendarList(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	const remoteURL = "https://apidata.googleusercontent.com/caldav/v2/me@example.com/events"
	seedCalendarColorState(t, db, q, remoteURL, "#112233", "#9e69af", 1)

	var sawPatch bool
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Host != "www.googleapis.com" {
			t.Fatalf("PATCH host = %q, want www.googleapis.com", r.URL.Host)
		}
		if !strings.Contains(r.URL.Path, "/calendar/v3/users/me/calendarList/") {
			t.Fatalf("PATCH path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("colorRgbFormat") != "true" {
			t.Fatalf("colorRgbFormat = %q", r.URL.Query().Get("colorRgbFormat"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !strings.Contains(string(body), `"backgroundColor":"#112233"`) {
			t.Fatalf("PATCH body = %s", body)
		}
		sawPatch = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"backgroundColor":"#112233"}`)),
			Request:    r,
		}, nil
	})

	if err := engine.syncCalendarMetadata(context.Background(), client, 1, remoteURL); err != nil {
		t.Fatalf("syncCalendarMetadata: %v", err)
	}
	if !sawPatch {
		t.Fatal("expected CalendarList color PATCH")
	}

	cal, err := q.GetCalendar(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Color != "#112233" {
		t.Fatalf("Color = %q, want #112233", cal.Color)
	}
	if got := storage.NullableToString(cal.RemoteColor); got != "#112233" {
		t.Fatalf("RemoteColor = %q, want #112233", got)
	}
	if cal.ColorDirty != 0 {
		t.Fatalf("ColorDirty = %d, want 0", cal.ColorDirty)
	}
}

// TestEngineSyncCalendarMetadataKeepsGoogleColorDirtyWhenPatchFails keeps
// the local Google color override when CalendarList PATCH is refused.
// Event sync must still proceed (issue #628).
func TestEngineSyncCalendarMetadataKeepsGoogleColorDirtyWhenPatchFails(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	const remoteURL = "https://apidata.googleusercontent.com/caldav/v2/me@example.com/events"
	seedCalendarColorState(t, db, q, remoteURL, "#112233", "#9e69af", 1)

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"reason":"forbidden"}}`)),
			Request:    r,
		}, nil
	})

	if err := engine.syncCalendarMetadata(context.Background(), client, 1, remoteURL); err != nil {
		t.Fatalf("syncCalendarMetadata: %v", err)
	}

	cal, err := q.GetCalendar(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Color != "#112233" {
		t.Fatalf("Color = %q, want #112233", cal.Color)
	}
	if cal.ColorDirty != 1 {
		t.Fatalf("ColorDirty = %d, want 1", cal.ColorDirty)
	}
}

// TestEngineSyncCalendarMetadataKeepsColorWhenRemoteOmitsIt reproduces
// issue #628. A 404 propstat for calendar-color must not wipe a color
// that discovery already stored.
func TestEngineSyncCalendarMetadataKeepsColorWhenRemoteOmitsIt(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	seedCalendarColorState(t, db, q, "https://example.com/cal/work", "#9e69af", "#9e69af", 0)

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
      <d:prop><ic:calendar-color/></d:prop>
      <d:status>HTTP/1.1 404 Not Found</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
			Request: r,
		}, nil
	})

	if err := engine.syncCalendarMetadata(context.Background(), client, 1, "https://example.com/cal/work"); err != nil {
		t.Fatalf("syncCalendarMetadata: %v", err)
	}

	cal, err := q.GetCalendar(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Color != "#9e69af" {
		t.Fatalf("Color = %q, want #9e69af", cal.Color)
	}
}

// TestEngineSyncCalendarPullsEventsWhenColorForbidden reproduces issue
// #628. Initial sync must pull events even when calendar-color PROPFIND
// returns HTTP 403.
func TestEngineSyncCalendarPullsEventsWhenColorForbidden(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	const eventICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:color-403-event
DTSTAMP:20260820T120000Z
DTSTART:20260820T120000Z
DTEND:20260820T130000Z
SUMMARY:Still synced
END:VEVENT
END:VCALENDAR
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPFIND":
			http.Error(w, `{"error":{"reason":"accessNotConfigured"}}`, http.StatusForbidden)
			return
		case "REPORT":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			if strings.Contains(string(body), "calendar-multiget") {
				_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/color-403-event.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-1&quot;</d:getetag>
        <cal:calendar-data>`+eventICS+`</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)
				return
			}
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/calendar/color-403-event.ics</d:href>
    <d:propstat>
      <d:prop><d:getetag>&quot;etag-1&quot;</d:getetag></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/1</d:sync-token>
</d:multistatus>`)
			return
		default:
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	remoteAccount, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "color-403", ServerUrl: server.URL, AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	calendars, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := calendars[0].ID
	remoteURL := server.URL + "/calendar"
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: calendarID, AccountID: &remoteAccount.ID, RemoteUrl: &remoteURL,
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE calendars SET color = '#9e69af', remote_color = '#9e69af', color_dirty = 0 WHERE id = ?
	`, calendarID); err != nil {
		t.Fatalf("seed calendar color: %v", err)
	}
	engine.credStore.(*mockCredStore).creds[remoteAccount.ID] = auth.Credential{
		AccountID: remoteAccount.ID, Username: "user", Password: "secret",
	}

	result, err := engine.SyncCalendar(ctx, calendarID, ConflictServerWins)
	if err != nil {
		t.Fatalf("SyncCalendar: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("sync errors = %v, want none after a color 403", result.Errors)
	}
	if result.Pulled != 1 {
		t.Fatalf("Pulled = %d, want 1", result.Pulled)
	}
	evt, err := q.GetEventByUID(ctx, "color-403-event")
	if err != nil {
		t.Fatalf("GetEventByUID: %v", err)
	}
	if evt.Title != "Still synced" {
		t.Fatalf("Title = %q, want Still synced", evt.Title)
	}
	cal, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Color != "#9e69af" {
		t.Fatalf("Color = %q, want #9e69af", cal.Color)
	}
}
