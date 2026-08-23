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

func seedCalendarColorState(t *testing.T, db *sql.DB, q *storage.Queries, remoteURL, color, remoteColor string, dirty int) {
	t.Helper()
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
		RemoteUrl: storage.StringToNullable(remoteURL),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE calendars
		SET color = ?, remote_color = ?, color_dirty = ?
		WHERE id = 1
	`, color, remoteColor, dirty); err != nil {
		t.Fatalf("seed calendar color state: %v", err)
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

// linkCalendarToTestAccount links the first seeded calendar to a fresh account.
// Service-layer mutations (and the simulated concurrent edit below) then flip
// the dirty flag via MarkResourceDirty. Returns the calendar ID.
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
// "etag-server"). That drives the ConflictServerWins accept-server path. A
// non-nil puts counter receives one increment per PUT.
func serverWinsConflictClient(t *testing.T, uid string, puts *int) *caldav.Client {
	t.Helper()
	path := "/calendar/" + uid + ".ics"
	return newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			if puts != nil {
				*puts++
			}
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

// emptyServerWinsConflictClient is like serverWinsConflictClient but its GET
// returns a VCALENDAR that carries only a VTIMEZONE. That is a non-empty body
// the encoder accepts, yet with no importable VEVENT/VTODO/VJOURNAL. It
// simulates a 412'd resource whose server body has nothing to apply
// (issue #495).
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

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins, false)
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
// issue #92. A concurrent local edit that arrives while the PUT is in flight
// must not be dropped in silence. Push exports the pre-edit body, PUTs it, and
// then clears the dirty flag. If the clear is unconditional it wipes the
// dirty=1 the concurrent edit set. The edit is then never pushed (lost update).
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

	if _, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins, false); err != nil {
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
// identity. It is now resolved from the already-loaded calendar and
// account rows instead of a re-query of the database. A non-empty (trimmed)
// owner_email wins. Otherwise the linked account's username is used. An
// unlinked calendar with no owner_email yields the empty string. The
// caller then skips the organizer gate.
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
// attendee PUTs (Google returns HTTP 400 with a vague <D:error/>). A
// retry of every sync is just dead weight. We clear the dirty flag and
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

	result, err := engine.push(ctx, client, calendarID, "/calendar/", "me@example.com", ConflictServerWins, false)
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
// sync_resource that points at a UID with no live event row stops the retry.
// This unblocks zombie rows left over from inconsistent state (e.g. user
// purged the local event but the sync_resource survived). It does not
// emit "get event by uid" errors on every sync run.
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

	result, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins, false)
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
// `<master>_R<rid>@google.com` orphan-instance pattern. The iCal stream
// gives an isolated occurrence with a synthetic suffixed UID and a
// RECURRENCE-ID. We import an override row but never receive a master.
// The exporter must still emit something pushable instead of an error.
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

// TestEngineExportResourcePropagatesOverrideListError guards a data-loss bug.
// exportResource used to discard the ListOverridesByUID error. For a recurring
// resource (master row + override rows that share the UID) a transient read
// error (e.g. SQLite busy/locked) on the override list would then be dropped
// in silence. GetByUID still supplied the master. The non-empty guard passed.
// The exporter produced a master-ONLY iCal. A PUT of that payload to the
// server overwrites and deletes every overridden occurrence. The export must
// fail instead of a partial body. We force the override read to fail. We seed
// a corrupt override row (non-numeric value in the INTEGER sequence
// column). The master lookup never reads that row. The override scan does.
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
// persistImported call flipped dirty=1. The event service's Replace*
// methods mark the sync_resource dirty as a side effect for user
// edits. UpsertSyncResource's `dirty = MAX(...)` clause preserved that
// 1. Every sync then re-dirtied resources it had just imported. The next
// push round-tripped them back to the server. The engine must explicitly
// clear dirty after a sync-driven import. The resource then lands clean.
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

// TestEnginePersistImportedKeepsDirtyOnChildReplaceError pins issue #69. A
// transient failure during a replace of an imported resource's child
// collections (alarms/attendees/...) must propagate out of persistImported.
// Previously the Replace* errors were discarded with `_ =`. The caller then
// cleared the dirty flag. The stale children were never retried. Here we let
// the parent UpsertByUID succeed but force ReplaceAlarms to fail (by a drop of
// the event_alarms table). Then assert persistImported returns an error. The
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

	if _, _, err := engine.persistImported(ctx, calendarID, result); err == nil {
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
