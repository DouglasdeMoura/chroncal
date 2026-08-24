package sync

import (
	"context"
	"database/sql"
	"errors"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestEnginePushConflictRecordFailureSurfacesError guards issue #610: a failed
// conflict insert must surface as an error and count nothing. The old code
// discarded the insert error with "_ =" and still counted the conflict, so
// callers printed notes about conflict rows that did not exist.
func TestEnginePushConflictRecordFailureSurfacesError(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "conflict-event")

	client := serverWinsConflictClient(t, "conflict-event", nil)

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

	// Force the conflict insert to fail after the 412 is detected.
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER fail_conflict_insert
		BEFORE INSERT ON sync_conflicts
		BEGIN
		    SELECT RAISE(ABORT, 'forced conflict insert failure');
		END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictPrompt, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(result.errors) != 1 || !strings.Contains(result.errors[0].Error(), "record conflict") {
		t.Fatalf("errors = %v, want one %q error", result.errors, "record conflict")
	}
	if result.conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0 (no row was recorded)", result.conflicts)
	}

	// The local edit must survive the failure: still dirty, nothing imported.
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "conflict-event"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Fatalf("Dirty = %d, want 1", res.Dirty)
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

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	// A server-wins full pass records the conflict row and then resolves it
	// in favor of the server. AutoResolved counts it; Conflicts counts only
	// rows recorded and left open. Issue #610, invariant: Conflicts equals
	// the open rows.
	if result.autoResolved != 1 {
		t.Fatalf("autoResolved = %d, want 1", result.autoResolved)
	}
	if result.conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", result.conflicts)
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

	// The conflict row stays, resolved "server-auto". The recorded local
	// body then remains recoverable via "sync resolve <id> --pick local".
	open, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open sync conflicts = %d, want 0", len(open))
	}
	allResolved, err := q.ListResolvedSyncConflicts(ctx)
	if err != nil {
		t.Fatalf("ListResolvedSyncConflicts: %v", err)
	}
	var resolvedRows []storage.SyncConflict
	for _, r := range allResolved {
		if r.CalendarID == calendarID {
			resolvedRows = append(resolvedRows, r)
		}
	}
	if len(resolvedRows) != 1 {
		t.Fatalf("resolved sync conflicts = %d, want 1", len(resolvedRows))
	}
	if resolvedRows[0].Resolution == nil || *resolvedRows[0].Resolution != ResolutionServerAuto {
		t.Fatalf("resolution = %v, want %q", resolvedRows[0].Resolution, ResolutionServerAuto)
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

func TestEngineProcessTombstonesTreatsGoneAsSuccess(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	deletes := 0
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete {
			t.Fatalf("unexpected method %s", r.Method)
		}
		deletes++
		switch r.URL.Path {
		case "/calendar/already-gone-404.ics":
			return newResponse(http.StatusNotFound, nil), nil
		case "/calendar/already-gone-410.ics":
			return newResponse(http.StatusGone, nil), nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	for _, tc := range []struct{ uid, path string }{
		{"already-gone-404", "/calendar/already-gone-404.ics"},
		{"already-gone-410", "/calendar/already-gone-410.ics"},
	} {
		if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
			CalendarID:   calendarID,
			Uid:          tc.uid,
			OwnerType:    "event",
			RemoteUrl:    tc.path,
			Etag:         "etag",
			SyncStrategy: "sync-token",
		}); err != nil {
			t.Fatalf("UpsertSyncResource %q: %v", tc.uid, err)
		}
		if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
			CalendarID: calendarID,
			Uid:        tc.uid,
			RemoteUrl:  tc.path,
		}); err != nil {
			t.Fatalf("CreateTombstone %q: %v", tc.uid, err)
		}
	}

	result, err := engine.processTombstones(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("processTombstones: %v", err)
	}
	// A resource already absent server-side (404/410) is the desired end
	// state, so the tombstone is cleared rather than retried forever.
	if result.deleted != 2 {
		t.Fatalf("deleted = %d, want 2", result.deleted)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %v, want none", result.errors)
	}
	if deletes != 2 {
		t.Fatalf("delete requests = %d, want 2 (no retry of an already-gone resource)", deletes)
	}

	tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListTombstonesByCalendar: %v", err)
	}
	if len(tombstones) != 0 {
		t.Fatalf("remaining tombstones = %d, want 0", len(tombstones))
	}
	resources, err := q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncResourcesByCalendar: %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("remaining sync resources = %d, want 0", len(resources))
	}
}

// TestEngineProcessTombstonesConflictCountsAutoResolved guards the 412 path
// of a tombstone delete. The remote edit wins (the tombstone is abandoned),
// so the outcome is an auto-resolution in favor of the server — not an open
// conflict row. SyncResult.AutoResolved counts it. Issue #610,
// invariant: AutoResolved equals the rows a pass settled on its own.
func TestEngineProcessTombstonesConflictCountsAutoResolved(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete {
			t.Fatalf("unexpected method %s", r.Method)
		}
		return newResponse(http.StatusPreconditionFailed, nil), nil
	})

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "delete-vs-edit",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/delete-vs-edit.ics",
		Etag:         `"etag-before"`,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID,
		Uid:        "delete-vs-edit",
		RemoteUrl:  "/calendar/delete-vs-edit.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}

	result, err := engine.processTombstones(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("processTombstones: %v", err)
	}
	if result.autoResolved != 1 {
		t.Fatalf("autoResolved = %d, want 1", result.autoResolved)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0", result.deleted)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %v, want none", result.errors)
	}

	// The tombstone is abandoned so the DELETE is not re-issued every sync.
	tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListTombstonesByCalendar: %v", err)
	}
	if len(tombstones) != 0 {
		t.Fatalf("remaining tombstones = %d, want 0", len(tombstones))
	}
	// The sync_resource survives so the next pull re-imports the remote edit.
	resources, err := q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncResourcesByCalendar: %v", err)
	}
	if len(resources) != 1 || resources[0].Uid != "delete-vs-edit" {
		t.Fatalf("remaining sync resources = %+v, want the conflicted one", resources)
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

	pushResult, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins, false)
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

// TestEnginePushEscapesUIDWhenAssigningNewResourcePath guards the PUT href
// for a UID with path-traversal bytes. The name comes from the UID, and the
// sanitizer percent-encodes each '/', so the wire request cannot escape the
// calendar collection. The decoded URL.Path still shows the raw bytes; the
// wire form is EscapedPath.
func TestEnginePushEscapesUIDWhenAssigningNewResourcePath(t *testing.T) {
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
		if got := r.URL.EscapedPath(); got != "/calendar/..%2F..%2Fescape.ics" {
			t.Fatalf("PUT path = %s, want /calendar/..%%2F..%%2Fescape.ics", got)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Status:     "201 Created",
			Header:     http.Header{"Etag": []string{`"etag-malicious"`}},
			Body:       io.NopCloser(http.NoBody),
			Request:    r,
		}, nil
	})

	pushResult, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins, false)
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
	if res.RemoteUrl != "/calendar/..%2F..%2Fescape.ics" {
		t.Fatalf("RemoteUrl = %q, want /calendar/..%%2F..%%2Fescape.ics", res.RemoteUrl)
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
