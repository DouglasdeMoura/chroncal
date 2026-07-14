package sync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
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

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins)
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

// TestEnginePushPreservesConcurrentEditDuringPut is the regression test for
// issue #92: a concurrent local edit that arrives while the PUT is in flight
// must not be silently dropped. Push exports the pre-edit body, PUTs it, and
// then clears the dirty flag. If the clear is unconditional it wipes the
// dirty=1 the concurrent edit set, so the edit is never pushed (lost update).
// The clear must be gated on the resource revision captured before the PUT.
func TestEnginePushPreservesConcurrentEditDuringPut(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	// Link the seeded calendar to an account so service-layer mutations
	// (here, the simulated concurrent edit) flip the dirty flag.
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	remoteCalURL := "https://example.com/cal"
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        calendarID,
		AccountID: &account.ID,
		RemoteUrl: &remoteCalURL,
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	insertTestEvent(t, db, calendarID, "concurrent-edit")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "concurrent-edit",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/concurrent-edit.ics",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut {
			// Simulate a user edit landing during the multi-second PUT
			// round-trip: the service-layer mutation marks the resource
			// dirty again. The exported body the server just received does
			// not contain this edit.
			if err := storage.MarkResourceDirty(ctx, db, calendarID, "concurrent-edit", "event"); err != nil {
				t.Fatalf("simulate concurrent edit: %v", err)
			}
		}
		return newResponse(http.StatusCreated, map[string]string{"ETag": `"etag-new"`}), nil
	})

	if _, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins); err != nil {
		t.Fatalf("push: %v", err)
	}

	// The concurrent edit must survive: the resource stays dirty so the next
	// push sends the edited body.
	dirty, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(dirty) != 1 {
		t.Fatalf("dirty after push = %d, want 1 (concurrent edit must not be dropped)", len(dirty))
	}
}

// TestResolvePushIdentity locks in the precedence rules for the push
// identity now that it is resolved from the already-loaded calendar and
// account rows instead of re-querying the database: a non-empty (trimmed)
// owner_email wins, otherwise the linked account's username is used, and
// an unlinked calendar with no owner_email yields the empty string so the
// caller skips the organizer gate.
func TestResolvePushIdentity(t *testing.T) {
	t.Parallel()

	accountID := int64(7)
	tests := []struct {
		name    string
		cal     storage.Calendar
		account storage.Account
		want    string
	}{
		{
			name:    "owner email wins",
			cal:     storage.Calendar{OwnerEmail: "owner@example.com", AccountID: &accountID},
			account: storage.Account{Username: "login@example.com"},
			want:    "owner@example.com",
		},
		{
			name:    "owner email trimmed",
			cal:     storage.Calendar{OwnerEmail: "  owner@example.com  ", AccountID: &accountID},
			account: storage.Account{Username: "login@example.com"},
			want:    "owner@example.com",
		},
		{
			name:    "falls back to account username",
			cal:     storage.Calendar{OwnerEmail: "   ", AccountID: &accountID},
			account: storage.Account{Username: "login@example.com"},
			want:    "login@example.com",
		},
		{
			name:    "no owner email and no account is empty",
			cal:     storage.Calendar{},
			account: storage.Account{Username: "login@example.com"},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolvePushIdentity(tt.cal, tt.account); got != tt.want {
				t.Fatalf("resolvePushIdentity() = %q, want %q", got, tt.want)
			}
		})
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

	result, err := engine.push(ctx, client, calendarID, "/calendar/", "me@example.com", ConflictServerWins)
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

	result, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins)
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

// TestEngineExportResourcePropagatesOverrideListError guards a data-loss bug:
// exportResource used to discard the ListOverridesByUID error. For a recurring
// resource (master row + override rows sharing the UID) a transient read error
// (e.g. SQLite busy/locked) on the override list would then be silently dropped
// — GetByUID still supplied the master, the non-empty guard passed, and the
// exporter produced a master-ONLY iCal. PUTting that payload to the server
// overwrites and deletes every overridden occurrence. The export must fail
// instead of emitting a partial body. We force the override read to fail by
// seeding a corrupt override row (non-numeric value in the INTEGER sequence
// column) that the master lookup never reads but the override scan does.
func TestEngineExportResourcePropagatesOverrideListError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ownerType string
		insertOK  string
		insertBad string
	}{
		{
			ownerType: "event",
			insertOK: "INSERT INTO events (uid, calendar_id, title, start_time, end_time) " +
				"VALUES (?, 1, 'Master', '2025-06-09T19:00:00Z', '2025-06-09T20:00:00Z')",
			insertBad: "INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id, sequence) " +
				"VALUES (?, 1, 'Override', '2025-06-09T19:00:00Z', '2025-06-09T20:00:00Z', '2025-06-09T19:00:00Z', 'not-an-int')",
		},
		{
			ownerType: "todo",
			insertOK:  "INSERT INTO todos (uid, calendar_id, summary) VALUES (?, 1, 'Master')",
			insertBad: "INSERT INTO todos (uid, calendar_id, summary, recurrence_id, sequence) " +
				"VALUES (?, 1, 'Override', '2025-06-09T19:00:00Z', 'not-an-int')",
		},
		{
			ownerType: "journal",
			insertOK:  "INSERT INTO journals (uid, calendar_id, summary) VALUES (?, 1, 'Master')",
			insertBad: "INSERT INTO journals (uid, calendar_id, summary, recurrence_id, sequence) " +
				"VALUES (?, 1, 'Override', '2025-06-09T19:00:00Z', 'not-an-int')",
		},
	}

	for _, tc := range cases {
		t.Run(tc.ownerType, func(t *testing.T) {
			t.Parallel()

			engine, db, _ := newTestEngine(t)
			ctx := context.Background()
			const uid = "recurring-uid"

			if _, err := db.ExecContext(ctx, tc.insertOK, uid); err != nil {
				t.Fatalf("insert master: %v", err)
			}
			if _, err := db.ExecContext(ctx, tc.insertBad, uid); err != nil {
				t.Fatalf("insert corrupt override: %v", err)
			}

			data, err := engine.exportResource(ctx, tc.ownerType, uid)
			if err == nil {
				t.Fatalf("exportResource returned nil error; master-only export would delete overrides on the server:\n%s", string(data))
			}
			if errors.Is(err, errResourceMissing) {
				t.Fatalf("exportResource reported missing resource, want the override read error: %v", err)
			}
		})
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

// TestEnginePersistImportedKeepsDirtyOnChildReplaceError pins issue #69: a
// transient failure while replacing an imported resource's child collections
// (alarms/attendees/...) must propagate out of persistImported. Previously the
// Replace* errors were discarded with `_ =`, so the caller cleared the dirty
// flag and the stale children were never retried. Here we let the parent
// UpsertByUID succeed but force ReplaceAlarms to fail (by dropping the
// event_alarms table), then assert persistImported returns an error and the
// sync_resource stays dirty so the next sync retries it.
func TestEnginePersistImportedKeepsDirtyOnChildReplaceError(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const uid = "child-replace-fail"

	// Seed a dirty sync_resource for the UID, mirroring a resource the pull
	// loop is about to absorb. If persistImported swallowed the child error,
	// the caller would clear this flag.
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          uid,
		OwnerType:    "event",
		RemoteUrl:    "/calendar/child-replace-fail.ics",
		Etag:         "etag-old",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// Drop the event_alarms table so the parent event upsert still succeeds but
	// the subsequent ReplaceAlarms fails, simulating a transient child-replace
	// error.
	if _, err := db.ExecContext(ctx, "DROP TABLE event_alarms"); err != nil {
		t.Fatalf("drop event_alarms table: %v", err)
	}

	result := icalPkg.ImportResult{
		Events: []event.Event{{
			UID:        uid,
			CalendarID: calendarID,
			Title:      "Has alarm",
			StartTime:  time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC),
			EndTime:    time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC),
			Alarms: []model.Alarm{{
				Action:       "DISPLAY",
				TriggerValue: "-PT15M",
				Description:  "Reminder",
				Related:      "START",
			}},
		}},
	}

	if _, err := engine.persistImported(ctx, calendarID, result); err == nil {
		t.Fatal("persistImported returned nil, want child-replace error to propagate")
	}

	dirty, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	var found bool
	for _, r := range dirty {
		if r.Uid == uid {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("resource %q no longer dirty after child-replace failure; sync would never retry", uid)
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

// TestEnginePullIncompletePullMarksCalendarUnhealthy reproduces issue #293: a
// pull that can never converge (here, an href the server keeps reporting as
// changed but that 404s on every multiget) used to only log and return no
// error, so SyncResult.Errors stayed empty and updateSyncHealth recorded the
// calendar as healthy — the ambient ⚠ glyph never lit up despite sync being
// permanently stuck. The incomplete pull must surface an error so the calendar
// is recorded unhealthy.
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
// test for issue #416: when loadCalendarClient returns early (missing
// credentials, no linked account, empty RemoteUrl) the updateSyncHealth defer
// used to be registered after that call, so it never ran. LastSyncAttemptedAt /
// LastSyncError stayed stale and the ambient ⚠ sidebar glyph (which keys on a
// non-empty LastSyncError) stayed dark while the calendar was permanently
// failing — notably OAuth calendars with revoked credentials.
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

// TestEnginePushSerializesConcurrentNewResourceCreate is the regression test
// for issue #225: two concurrent push runs for the same calendar (e.g. an
// opportunistic save-time PushCalendar racing a periodic SyncCalendar) must not
// both create a server object for the same never-pushed, etag-less resource.
// Before the per-calendar push lock, each run read the same dirty
// sync_resource (RemoteUrl=""), minted a distinct random href, and PUT it with
// no If-Match precondition, leaving the server with two objects for one UID.
//
// The two runs use distinct Engine instances over a shared DB, mirroring the
// TUI, which builds a fresh sync.Service per operation (see newSyncService) —
// an Engine-scoped lock would not catch this; the lock registry is keyed by DB.
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
			if _, err := engines[i].push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins); err != nil {
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

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictPrompt)
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

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictPrompt)
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

// TestEnginePushSkipsUIDWithOpenConflict verifies that once a prompt-mode
// conflict has been recorded for a UID, subsequent syncs do not re-PUT the
// still-dirty resource and do not insert duplicate sync_conflicts rows. See
// issue #104: the original code left the resource dirty with its stale ETag,
// so every tick issued a wasted failing PUT and appended another conflict row.
func TestEnginePushSkipsUIDWithOpenConflict(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "conflict-event")

	var puts int
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			puts++
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

	// First sync: detects the 412 and records the conflict.
	if _, err := engine.push(ctx, client, calendarID, "", "", ConflictPrompt); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if puts != 1 {
		t.Fatalf("PUTs after first push = %d, want 1", puts)
	}

	// Second sync: the conflict is still unresolved, so the resource must be
	// skipped entirely — no second PUT, no duplicate conflict row.
	result, err := engine.push(ctx, client, calendarID, "", "", ConflictPrompt)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if puts != 1 {
		t.Fatalf("PUTs after second push = %d, want 1 (resource with open conflict must not be re-PUT)", puts)
	}
	if result.conflicts != 0 {
		t.Fatalf("second push conflicts = %d, want 0", result.conflicts)
	}

	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("sync conflicts = %d, want 1 (no duplicate rows)", len(conflicts))
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

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins)
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

	pushResult, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins)
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

	pushResult, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins)
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

	result, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins)
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
// whose local apply() fails — e.g. a transient SQLITE_BUSY, simulated here by
// dropping the events table — must NOT advance the sync-token. Otherwise the
// orphaned local row survives forever: the server is now behind the new token
// and never re-reports the deletion, so there is no retry. The token must be
// withheld so the next sync re-lists the same 404 and apply gets another shot.
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

// TestEngineSyncCalendarMetadataSkipsFetchWhenDirty reproduces issue #419: when
// the local color is dirty, syncCalendarMetadata must not waste a PROPFIND to
// fetch the remote color (whose value would be discarded anyway), and a failure
// of that fetch must not block the pending color push. The mock server fails any
// PROPFIND with 503; the push must still happen and ColorDirty must clear.
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

// TestPersistImportedClearsRemovedAlarms is a regression test for issue #65:
// a CalDAV pull that re-imports an existing UID whose server component no
// longer carries an alarm must clear the locally stored alarm. Before the
// fix, persistImported only replaced child collections when the server sent a
// non-empty list, so server-side removals were silently dropped and stale
// alarms lingered.
func TestPersistImportedClearsRemovedAlarms(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const withAlarm = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:alarm-removal-uid
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Has an alarm
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
DESCRIPTION:Meeting reminder
END:VALARM
END:VEVENT
END:VCALENDAR
`

	withAlarmResult, err := icalPkg.ImportFile(strings.NewReader(withAlarm))
	if err != nil {
		t.Fatalf("ImportFile (with alarm): %v", err)
	}
	if _, err := engine.persistImported(ctx, calendarID, withAlarmResult); err != nil {
		t.Fatalf("persistImported (with alarm): %v", err)
	}

	saved, err := q.GetEventByUID(ctx, "alarm-removal-uid")
	if err != nil {
		t.Fatalf("GetEventByUID: %v", err)
	}
	alarms, err := engine.events.ListAlarms(ctx, saved.ID)
	if err != nil {
		t.Fatalf("ListAlarms (after first import): %v", err)
	}
	if len(alarms) != 1 {
		t.Fatalf("alarms after first import = %d, want 1", len(alarms))
	}

	// Re-import the same UID with no VALARM: the server dropped the alarm.
	const noAlarm = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:alarm-removal-uid
DTSTAMP:20260403T140000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Alarm removed on server
END:VEVENT
END:VCALENDAR
`

	noAlarmResult, err := icalPkg.ImportFile(strings.NewReader(noAlarm))
	if err != nil {
		t.Fatalf("ImportFile (no alarm): %v", err)
	}
	if _, err := engine.persistImported(ctx, calendarID, noAlarmResult); err != nil {
		t.Fatalf("persistImported (no alarm): %v", err)
	}

	alarms, err = engine.events.ListAlarms(ctx, saved.ID)
	if err != nil {
		t.Fatalf("ListAlarms (after re-import): %v", err)
	}
	if len(alarms) != 0 {
		t.Fatalf("alarms after server-side removal = %d, want 0 (stale alarm not cleared)", len(alarms))
	}
}

// TestEnginePullWithholdsTokenOnPersistFailure covers issue #103: when a
// fetched resource is successfully multiget'd but fails to persist locally
// (a transient SQLite busy/lock or a child-replace error), the pull must NOT
// advance the sync-token. Otherwise the token moves past the failed change
// and the next REPORT never re-lists it, so the server-side update is lost
// from the local copy indefinitely. The resource's old etag and the calendar
// sync-token must both stay put so the next sync re-lists and retries.
func TestEnginePullWithholdsTokenOnPersistFailure(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	// A locally-tracked event the server has just updated. Its multiget body
	// carries a VALARM; dropping event_alarms below makes persistImported fail
	// on ReplaceAlarms after the parent upsert, simulating a transient persist
	// error mid-pull.
	insertTestEvent(t, db, calendarID, "victim")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "victim", OwnerType: "event",
		RemoteUrl: "/calendar/victim.ics", Etag: "old", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource victim: %v", err)
	}

	const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/calendar/victim.ics</d:href>
    <d:propstat><d:prop><d:getetag>&quot;new&quot;</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/t1</d:sync-token>
</d:multistatus>`

	const victimICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:victim
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Updated meeting
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
DESCRIPTION:Reminder
END:VALARM
END:VEVENT
END:VCALENDAR
`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "calendar-multiget") {
			return &http.Response{StatusCode: http.StatusMultiStatus, Status: "207 Multi-Status",
				Header: http.Header{"Content-Type": []string{"application/xml"}},
				Body:   io.NopCloser(strings.NewReader(syncBody)), Request: r}, nil
		}
		multigetBody := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/victim.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;new&quot;</d:getetag>
        <cal:calendar-data>` + victimICS + `</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
		return &http.Response{StatusCode: http.StatusMultiStatus, Status: "207 Multi-Status",
			Header: http.Header{"Content-Type": []string{"application/xml"}},
			Body:   io.NopCloser(strings.NewReader(multigetBody)), Request: r}, nil
	})

	// Force the persist to fail: drop event_alarms so ReplaceAlarms errors
	// after the parent event upsert succeeds.
	if _, err := db.ExecContext(ctx, "DROP TABLE event_alarms"); err != nil {
		t.Fatalf("drop event_alarms table: %v", err)
	}

	if _, err := engine.pull(ctx, client, calendarID, "/calendar/"); err != nil {
		t.Fatalf("pull: %v", err)
	}

	// Token must be held back so the next sync re-lists the failed change.
	calRow, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if tok := storage.NullableToString(calRow.SyncToken); tok != "" {
		t.Fatalf("sync_token = %q, want empty (held back on persist failure)", tok)
	}

	// The resource's etag must stay old so the next REPORT still sees a diff.
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "victim"})
	if err != nil {
		t.Fatalf("GetSyncResource victim: %v", err)
	}
	if res.Etag != "old" {
		t.Fatalf("victim etag = %q, want old preserved (persist failed)", res.Etag)
	}
}

// TestPersistImportedRollsBackOnReplaceFailure verifies that persistImported is
// atomic per resource: if any Replace* step fails after the event row and some
// of its child collections have already been written, the entire resource is
// rolled back rather than left in a partial state.
func TestPersistImportedRollsBackOnReplaceFailure(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const uid = "atomic-import"
	result := icalPkg.ImportResult{
		Events: []event.Event{{
			UID:       uid,
			Title:     "Meeting",
			StartTime: time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC),
			Status:    "CONFIRMED",
			Transp:    "OPAQUE",
			Class:     "PUBLIC",
			Alarms: []model.Alarm{{
				Action:       "DISPLAY",
				TriggerValue: "-PT15M",
				Description:  "Reminder",
				Related:      "START",
			}},
			Attendees: []model.Attendee{{Email: "a@example.com"}},
			Comments:  []string{"note"},
		}},
	}

	// Force the ReplaceComments step (which runs after the event upsert and
	// after ReplaceAlarms/ReplaceAttendees succeed) to fail mid-sequence,
	// mirroring a transient DB error.
	if _, err := db.ExecContext(ctx, "DROP TABLE event_comments"); err != nil {
		t.Fatalf("drop event_comments: %v", err)
	}

	if _, err := engine.persistImported(ctx, calendarID, result); err == nil {
		t.Fatal("expected persistImported to fail when a Replace step errors")
	}

	// The whole resource must roll back: no partial event row left behind.
	if _, err := engine.events.GetByUID(ctx, uid); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected event %q to be absent after rollback, got err=%v", uid, err)
	}
}

// TestLookupOwnerIDUnknownTypeErrors guards the owner-type dispatch: an
// unrecognized owner-type string must fail loudly rather than silently
// resolving to ID 0, which would mis-attribute a sync conflict record.
func TestLookupOwnerIDUnknownTypeErrors(t *testing.T) {
	t.Parallel()

	engine, _, _ := newTestEngine(t)
	ctx := context.Background()

	id, err := engine.lookupOwnerID(ctx, "bogus", "some-uid")
	if !errors.Is(err, errUnknownOwnerType) {
		t.Fatalf("lookupOwnerID(bogus) err = %v, want errUnknownOwnerType", err)
	}
	if id != 0 {
		t.Fatalf("lookupOwnerID(bogus) id = %d, want 0", id)
	}
}

// TestLookupOwnerIDResolvesByType confirms a known owner type resolves its row
// ID, and that a missing UID surfaces the lookup error instead of 0.
func TestLookupOwnerIDResolvesByType(t *testing.T) {
	t.Parallel()

	engine, db, _ := newTestEngine(t)
	ctx := context.Background()

	const uid = "lookup-evt-1"
	insertTestEvent(t, db, 1, uid)

	want, err := engine.events.GetByUID(ctx, uid)
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}

	got, err := engine.lookupOwnerID(ctx, "event", uid)
	if err != nil {
		t.Fatalf("lookupOwnerID(event): %v", err)
	}
	if got != want.ID {
		t.Fatalf("lookupOwnerID(event) = %d, want %d", got, want.ID)
	}

	if _, err := engine.lookupOwnerID(ctx, "event", "missing-uid"); err == nil {
		t.Fatal("lookupOwnerID(event, missing-uid) err = nil, want lookup error")
	}
}

// TestOwnerDispatchRejectsUnknownTypeUniformly confirms every owner-type
// dispatch entry point reports an unknown type through the same error, so a
// new component type can't be silently skipped by one site.
func TestOwnerDispatchRejectsUnknownTypeUniformly(t *testing.T) {
	t.Parallel()

	engine, _, _ := newTestEngine(t)
	ctx := context.Background()

	if err := engine.deleteLocalResourceByUID(ctx, "bogus", "uid"); !errors.Is(err, errUnknownOwnerType) {
		t.Fatalf("deleteLocalResourceByUID err = %v, want errUnknownOwnerType", err)
	}
	if _, err := engine.lookupOwnerID(ctx, "bogus", "uid"); !errors.Is(err, errUnknownOwnerType) {
		t.Fatalf("lookupOwnerID err = %v, want errUnknownOwnerType", err)
	}
	if _, err := engine.exportResource(ctx, "bogus", "uid"); !errors.Is(err, errUnknownOwnerType) {
		t.Fatalf("exportResource err = %v, want errUnknownOwnerType", err)
	}
}

// linkCalendarToTestAccount links the first seeded calendar to a fresh account
// so service-layer mutations (and the simulated concurrent edit below) flip the
// dirty flag via MarkResourceDirty. Returns the calendar ID.
func linkCalendarToTestAccount(t *testing.T, ctx context.Context, q *storage.Queries) int64 {
	t.Helper()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	remoteCalURL := "https://example.com/cal"
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        calendarID,
		AccountID: &account.ID,
		RemoteUrl: &remoteCalURL,
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	return calendarID
}

// serverWinsConflictClient returns a CalDAV client whose PUT 412s and whose GET
// returns the server's version of uid (SUMMARY "Server version", ETag
// "etag-server"), driving the ConflictServerWins accept-server path.
func serverWinsConflictClient(t *testing.T, uid string) *caldav.Client {
	t.Helper()
	path := "/calendar/" + uid + ".ics"
	return newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			return &http.Response{
				StatusCode: http.StatusPreconditionFailed,
				Status:     "412 Precondition Failed",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("precondition failed")),
				Request:    r,
			}, nil
		case http.MethodGet:
			if r.URL.Path != path {
				t.Fatalf("GET path = %s, want %s", r.URL.Path, path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header: http.Header{
					"Content-Type": []string{"text/calendar; charset=utf-8"},
					"Etag":         []string{`"etag-server"`},
				},
				Body: io.NopCloser(strings.NewReader("BEGIN:VCALENDAR\r\n" +
					"VERSION:2.0\r\n" +
					"PRODID:-//chroncal//tests//EN\r\n" +
					"BEGIN:VEVENT\r\n" +
					"UID:" + uid + "\r\n" +
					"DTSTAMP:20260403T120000Z\r\n" +
					"DTSTART:20260403T120000Z\r\n" +
					"DTEND:20260403T130000Z\r\n" +
					"SUMMARY:Server version\r\n" +
					"END:VEVENT\r\n" +
					"END:VCALENDAR\r\n")),
				Request: r,
			}, nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})
}

// TestEnginePushServerWinsPreservesConcurrentEdit reproduces issue #417: a local
// edit landing in the window between the accept-server import and the dirty
// clear must not be silently dropped. The afterImportRevCapture hook simulates
// that edit; the rev-guarded clear must leave the resource dirty so the next
// push sends it. With the previous unconditional clear this test fails because
// the edit's dirty flag is wiped. Serial (no t.Parallel) because it mutates the
// package-level hook.
func TestEnginePushServerWinsPreservesConcurrentEdit(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := linkCalendarToTestAccount(t, ctx, q)

	insertTestEvent(t, db, calendarID, "srv-wins-race")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "srv-wins-race",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/srv-wins-race.ics",
		Etag:         `"etag-before"`,
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// Simulate a concurrent local edit landing after the import recorded the
	// server version but before the dirty flag is cleared: it bumps rev and
	// re-marks the resource dirty, exactly as a real service-layer mutation would.
	var fired int
	afterImportRevCapture = func() {
		fired++
		if err := storage.MarkResourceDirty(ctx, db, calendarID, "srv-wins-race", "event"); err != nil {
			t.Errorf("simulate concurrent edit: %v", err)
		}
	}
	t.Cleanup(func() { afterImportRevCapture = nil })

	client := serverWinsConflictClient(t, "srv-wins-race")
	if _, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins); err != nil {
		t.Fatalf("push: %v", err)
	}
	if fired != 1 {
		t.Fatalf("afterImportRevCapture fired %d times, want 1", fired)
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "srv-wins-race"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Fatalf("dirty = %d, want 1 (concurrent edit must not be dropped, #417)", res.Dirty)
	}
	// The ETag still advances to the server's version so the next push's If-Match
	// matches the server, mirroring FinalizePushedResource on the push path.
	if res.Etag != "etag-server" {
		t.Fatalf("etag = %q, want %q", res.Etag, "etag-server")
	}
}

// TestEnginePushServerWinsPreservesConcurrentEditAfterPersist reproduces issue
// #494: a local edit that commits between the accept-server import's persist
// commit and the dirty clear must not be silently dropped. The afterImportPersist
// hook fires in that window — after persistImported committed, before
// clearDirtyAfterImport — and bumps rev + re-marks dirty exactly as a real
// service-layer mutation would. Because persistImported now captures the
// post-import rev inside its own transaction (rather than re-reading it after
// commit, where this edit's bump would be read and matched), the rev-guarded
// clear leaves the resource dirty. With the old after-commit re-read this test
// fails: the clear reads the edit's bumped rev and wipes dirty. Serial (no
// t.Parallel) because it mutates the package-level hook.
func TestEnginePushServerWinsPreservesConcurrentEditAfterPersist(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := linkCalendarToTestAccount(t, ctx, q)

	insertTestEvent(t, db, calendarID, "srv-wins-persist-race")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "srv-wins-persist-race",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/srv-wins-persist-race.ics",
		Etag:         `"etag-before"`,
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// Simulate a concurrent local edit landing after the import committed but
	// before the dirty clear. persistImported already released its connection,
	// so this auto-commit write is safe under SetMaxOpenConns(1).
	var fired int
	afterImportPersist = func() {
		fired++
		if err := storage.MarkResourceDirty(ctx, db, calendarID, "srv-wins-persist-race", "event"); err != nil {
			t.Errorf("simulate concurrent edit: %v", err)
		}
	}
	t.Cleanup(func() { afterImportPersist = nil })

	client := serverWinsConflictClient(t, "srv-wins-persist-race")
	if _, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins); err != nil {
		t.Fatalf("push: %v", err)
	}
	if fired != 1 {
		t.Fatalf("afterImportPersist fired %d times, want 1", fired)
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "srv-wins-persist-race"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Fatalf("dirty = %d, want 1 (concurrent edit must not be dropped, #494)", res.Dirty)
	}
	// The ETag still advances to the server's version, mirroring
	// FinalizePushedResource on the push path.
	if res.Etag != "etag-server" {
		t.Fatalf("etag = %q, want %q", res.Etag, "etag-server")
	}
}

// emptyServerWinsConflictClient is like serverWinsConflictClient but its GET
// returns a VCALENDAR carrying only a VTIMEZONE — a non-empty body the encoder
// accepts, yet with no importable VEVENT/VTODO/VJOURNAL, simulating a 412'd
// resource whose server body has nothing to apply (issue #495).
func emptyServerWinsConflictClient(t *testing.T, uid string) *caldav.Client {
	t.Helper()
	path := "/calendar/" + uid + ".ics"
	return newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			return &http.Response{
				StatusCode: http.StatusPreconditionFailed,
				Status:     "412 Precondition Failed",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("precondition failed")),
				Request:    r,
			}, nil
		case http.MethodGet:
			if r.URL.Path != path {
				t.Fatalf("GET path = %s, want %s", r.URL.Path, path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header: http.Header{
					"Content-Type": []string{"text/calendar; charset=utf-8"},
					"Etag":         []string{`"etag-server"`},
				},
				Body: io.NopCloser(strings.NewReader("BEGIN:VCALENDAR\r\n" +
					"VERSION:2.0\r\n" +
					"PRODID:-//chroncal//tests//EN\r\n" +
					"BEGIN:VTIMEZONE\r\n" +
					"TZID:UTC\r\n" +
					"BEGIN:STANDARD\r\n" +
					"DTSTART:19700101T000000\r\n" +
					"TZOFFSETFROM:+0000\r\n" +
					"TZOFFSETTO:+0000\r\n" +
					"END:STANDARD\r\n" +
					"END:VTIMEZONE\r\n" +
					"END:VCALENDAR\r\n")),
				Request: r,
			}, nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})
}

// TestEnginePushServerWinsKeepsDirtyWhenServerBodyEmpty reproduces issue #495: on
// a 412 with ConflictServerWins, if the re-fetched server body carries no
// importable VEVENT/VTODO/VJOURNAL, importICal applies nothing. The auto-resolve
// must not clear dirty or stamp the server ETag — doing so would drop the local
// edit behind a server version that was never adopted, the asymmetry the manual
// ResolveConflict path already guards against (#466). With the previous
// unconditional clear this test fails because dirty is wiped and the ETag is
// advanced without anything applied.
func TestEnginePushServerWinsKeepsDirtyWhenServerBodyEmpty(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := linkCalendarToTestAccount(t, ctx, q)

	insertTestEvent(t, db, calendarID, "srv-wins-empty")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "srv-wins-empty",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/srv-wins-empty.ics",
		Etag:         `"etag-before"`,
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	client := emptyServerWinsConflictClient(t, "srv-wins-empty")
	if _, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins); err != nil {
		t.Fatalf("push: %v", err)
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "srv-wins-empty"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Fatalf("dirty = %d, want 1 (nothing was applied, local edit must survive, #495)", res.Dirty)
	}
	// The ETag must NOT be stamped to the server version: nothing from the server
	// was adopted, so claiming the local row matches the server would let the next
	// pull overwrite the still-pending local edit.
	if res.Etag != `"etag-before"` {
		t.Fatalf("etag = %q, want %q (server version was never applied, #495)", res.Etag, `"etag-before"`)
	}
}

// TestPersistImportedPrunesStaleOverrides verifies that when a CalDAV server
// deletes a recurring instance, persistImported soft-deletes the stale local
// override row. A server signals instance deletion by removing the override
// VEVENT from the resource and adding the slot to the master's EXDATE. Without
// pruning, the stale override is still CONFIRMED and expansion resurrects it —
// the orphan checker deliberately ignores EXDATEs so a legitimate override is
// never mistaken for an orphan.
func TestPersistImportedPrunesStaleOverrides(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const uid = "biweekly-prune-test"
	const deletedRID = "2026-07-02T17:00:00Z"
	const keptRID = "2026-08-27T17:00:00Z"

	// First sync: master + two overrides.
	_, err = engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{
			pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
			pruneTestEvent(uid, calendarID, deletedRID, ""),
			pruneTestEvent(uid, calendarID, keptRID, ""),
		},
	})
	if err != nil {
		t.Fatalf("first persistImported: %v", err)
	}

	overrides, err := q.ListOverridesByUID(ctx, uid)
	if err != nil {
		t.Fatalf("ListOverridesByUID after first import: %v", err)
	}
	if len(overrides) != 2 {
		t.Fatalf("expected 2 live overrides after first import, got %d", len(overrides))
	}

	// Second sync: server deleted the 7/2 instance (EXDATE on master, override
	// VEVENT removed) but kept the 8/27 override.
	secondMaster := pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH")
	secondMaster.ExDates = deletedRID
	_, err = engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{
			secondMaster,
			pruneTestEvent(uid, calendarID, keptRID, ""),
		},
	})
	if err != nil {
		t.Fatalf("second persistImported: %v", err)
	}

	// Only the kept override should remain live.
	overrides, err = q.ListOverridesByUID(ctx, uid)
	if err != nil {
		t.Fatalf("ListOverridesByUID after second import: %v", err)
	}
	if len(overrides) != 1 || overrides[0].RecurrenceID != keptRID {
		got := make([]string, 0, len(overrides))
		for _, o := range overrides {
			got = append(got, o.RecurrenceID)
		}
		t.Fatalf("expected 1 live override %q, got %v", keptRID, got)
	}

	// The stale override should be soft-deleted (not hard-deleted).
	deletedRIDs, err := q.ListDeletedOverrideRecurrenceIDs(ctx, uid)
	if err != nil {
		t.Fatalf("ListDeletedOverrideRecurrenceIDs: %v", err)
	}
	if !slices.Contains(deletedRIDs, deletedRID) {
		t.Fatalf("stale override %q was not soft-deleted; deleted = %v", deletedRID, deletedRIDs)
	}

	// The master should still exist with the EXDATE.
	master, err := q.GetEventByUID(ctx, uid)
	if err != nil {
		t.Fatalf("GetEventByUID after second import: %v", err)
	}
	if master.Exdates == nil || *master.Exdates == "" {
		t.Fatalf("master EXDATE not set; expected %q", deletedRID)
	}
}

// pruneTestEvent builds a minimal imported event for override-prune tests. A
// non-empty rid must be an RFC 3339 time; it doubles as the instance start.
func pruneTestEvent(uid string, calendarID int64, rid, rrule string) event.Event {
	start := time.Date(2026, 6, 18, 17, 0, 0, 0, time.UTC)
	if rid != "" {
		parsed, err := time.Parse(time.RFC3339, rid)
		if err != nil {
			panic(err)
		}
		start = parsed
	}
	return event.Event{
		UID:            uid,
		CalendarID:     calendarID,
		Title:          "Prune " + uid,
		StartTime:      start,
		EndTime:        start.Add(time.Hour),
		RecurrenceRule: rrule,
		RecurrenceID:   rid,
	}
}

// seedCleanSyncResource records uid as a synced, clean (dirty=0) resource,
// the state a completed pull leaves behind.
func seedCleanSyncResource(t *testing.T, q *storage.Queries, calendarID int64, uid string) {
	t.Helper()
	ctx := context.Background()
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: uid, OwnerType: "event",
		RemoteUrl: "https://example.com/cal/" + uid + ".ics", Etag: "v1",
		Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
		Etag: "v1", CalendarID: calendarID, Uid: uid,
	}); err != nil {
		t.Fatalf("ClearSyncResourceDirty: %v", err)
	}
}

// A resource that was dirty before the import carries unpushed local changes.
// A locally created override is absent from the server body because the
// server has never seen it — pruning it would silently discard the edit, so
// the prune must skip dirty resources.
func TestPersistImportedPruneSkipsDirtyResource(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	testutil.LinkCalendarToAccount(t, db)
	calendarID := int64(1) // LinkCalendarToAccount links the seeded calendar 1

	const uid = "dirty-prune-test"
	const localRID = "2026-07-02T17:00:00Z"

	// First pull: master only, then the tracking row settles clean.
	if _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH")},
	}); err != nil {
		t.Fatalf("first persistImported: %v", err)
	}
	seedCleanSyncResource(t, q, calendarID, uid)

	// Local, not-yet-pushed instance edit: a new override row plus dirty=1.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class, recurrence_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		uid, calendarID, "Local edit", localRID, "2026-07-02T18:00:00Z",
		"CONFIRMED", "OPAQUE", "PUBLIC", localRID,
	); err != nil {
		t.Fatalf("insert local override: %v", err)
	}
	if err := q.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{
		CalendarID: calendarID, Uid: uid,
	}); err != nil {
		t.Fatalf("MarkSyncResourceDirty: %v", err)
	}

	// Second pull before the push lands (e.g. the series title changed on the
	// server). The server body has no override — because it has never seen it.
	if _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH")},
	}); err != nil {
		t.Fatalf("second persistImported: %v", err)
	}

	overrides, err := q.ListOverridesByUID(ctx, uid)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 || overrides[0].RecurrenceID != localRID {
		t.Fatalf("unpushed local override was pruned; live overrides = %d", len(overrides))
	}
}

// The dirty gate must not block the normal case: a clean synced resource whose
// server body dropped an override still prunes the stale row.
func TestPersistImportedPrunesCleanSyncedResource(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	testutil.LinkCalendarToAccount(t, db)
	calendarID := int64(1) // LinkCalendarToAccount links the seeded calendar 1

	const uid = "clean-prune-test"
	const staleRID = "2026-07-02T17:00:00Z"

	if _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{
			pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
			pruneTestEvent(uid, calendarID, staleRID, ""),
		},
	}); err != nil {
		t.Fatalf("first persistImported: %v", err)
	}
	seedCleanSyncResource(t, q, calendarID, uid)

	// Server deleted the instance: EXDATE on the master, override gone.
	master := pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH")
	master.ExDates = staleRID
	if _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{master},
	}); err != nil {
		t.Fatalf("second persistImported: %v", err)
	}

	overrides, err := q.ListOverridesByUID(ctx, uid)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 0 {
		t.Fatalf("stale override not pruned on clean resource; live overrides = %d", len(overrides))
	}
}

// A component the parser dropped is absent from ImportResult without being
// absent from the server, so a non-zero SkippedComponents must disable
// pruning for the whole result.
func TestPersistImportedPruneSkipsIncompleteParse(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const uid = "partial-parse-test"
	const rid = "2026-07-02T17:00:00Z"

	if _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{
			pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
			pruneTestEvent(uid, calendarID, rid, ""),
		},
	}); err != nil {
		t.Fatalf("first persistImported: %v", err)
	}

	// Second pull: the override VEVENT was dropped by the parser, not by the
	// server. The keep-set is an incomplete inventory — no pruning.
	if _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events:            []event.Event{pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH")},
		SkippedComponents: 1,
	}); err != nil {
		t.Fatalf("second persistImported: %v", err)
	}

	overrides, err := q.ListOverridesByUID(ctx, uid)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("override pruned despite incomplete parse; live overrides = %d", len(overrides))
	}
}

// A resource holding components of more than one UID must reconcile each UID
// against its own master: UID A's master says nothing about UID B's override
// inventory.
func TestPersistImportedPrunePerUID(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const uidA = "multi-uid-a"
	const uidB = "multi-uid-b"
	const ridA = "2026-07-02T17:00:00Z"
	const ridB = "2026-07-03T17:00:00Z"

	for uid, rid := range map[string]string{uidA: ridA, uidB: ridB} {
		if _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
			Events: []event.Event{
				pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;BYDAY=TH"),
				pruneTestEvent(uid, calendarID, rid, ""),
			},
		}); err != nil {
			t.Fatalf("seed persistImported %q: %v", uid, err)
		}
	}

	// One malformed resource: B's override listed before A's master, and B's
	// master absent. Only A — whose own master is present — may be pruned.
	if _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{
			pruneTestEvent(uidB, calendarID, ridB, ""),
			pruneTestEvent(uidA, calendarID, "", "FREQ=WEEKLY;BYDAY=TH"),
		},
	}); err != nil {
		t.Fatalf("mixed persistImported: %v", err)
	}

	overridesA, err := q.ListOverridesByUID(ctx, uidA)
	if err != nil {
		t.Fatalf("ListOverridesByUID(A): %v", err)
	}
	if len(overridesA) != 0 {
		t.Fatalf("A's stale override not pruned; live overrides = %d", len(overridesA))
	}
	overridesB, err := q.ListOverridesByUID(ctx, uidB)
	if err != nil {
		t.Fatalf("ListOverridesByUID(B): %v", err)
	}
	if len(overridesB) != 1 {
		t.Fatalf("B's override pruned without B's master present; live overrides = %d", len(overridesB))
	}
}

func TestEngineSyncCalendarReadOnlyPullsWithoutRemoteWrites(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	var (
		mu      gosync.Mutex
		methods []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		if r.Method != "REPORT" {
			http.Error(w, "read-only collection rejects metadata and writes", http.StatusForbidden)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "sync-collection") {
			http.Error(w, "sync-collection unsupported", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"></d:multistatus>`)
	}))
	defer server.Close()

	remoteAccount, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "read-only", ServerUrl: server.URL, AuthType: "basic", Username: "user",
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
	if _, err := db.ExecContext(ctx, "UPDATE calendars SET remote_access = 'read' WHERE id = ?", calendarID); err != nil {
		t.Fatalf("set remote access: %v", err)
	}
	engine.credStore.(*mockCredStore).creds[remoteAccount.ID] = auth.Credential{
		AccountID: remoteAccount.ID, Username: "user", Password: "secret",
	}

	result, err := engine.SyncCalendar(ctx, calendarID, ConflictServerWins)
	if err != nil {
		t.Fatalf("SyncCalendar: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("read-only sync errors = %v", result.Errors)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, method := range methods {
		if method != "REPORT" {
			t.Fatalf("read-only sync sent %s; methods = %v", method, methods)
		}
	}
	if len(methods) == 0 {
		t.Fatal("read-only sync must still pull with REPORT")
	}
}
