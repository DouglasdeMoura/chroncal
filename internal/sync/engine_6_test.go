package sync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"
)

// TestEnginePushServerWinsPreservesConcurrentEditAfterPersist reproduces issue
// #494. A local edit that commits between the accept-server import's persist
// commit and the dirty clear must not be dropped in silence. The engine's
// afterImportPersist hook fires in that window, after persistImported committed
// and before clearDirtyAfterImport. It bumps rev and re-marks dirty exactly as
// a real service-layer mutation would. persistImported now captures the
// post-import rev inside its own transaction. It does not re-read it after
// commit, where this edit's bump would be read and matched. The rev-guarded
// clear then leaves the resource dirty. With the old after-commit re-read this
// test fails. The clear reads the edit's bumped rev and wipes dirty.
func TestEnginePushServerWinsPreservesConcurrentEditAfterPersist(t *testing.T) {
	t.Parallel()

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
	engine.testHooks = &engineTestHooks{
		afterImportPersist: func() {
			fired++
			if err := storage.MarkResourceDirty(ctx, db, calendarID, "srv-wins-persist-race", "event"); err != nil {
				t.Errorf("simulate concurrent edit: %v", err)
			}
		},
	}

	client := serverWinsConflictClient(t, "srv-wins-persist-race", nil)
	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins, false)
	if err != nil {
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

	// Dirty survived, so the recorded conflict row stays open (see the
	// srv-wins-race twin above).
	if result.conflicts != 1 || result.autoResolved != 0 {
		t.Fatalf("result = conflicts %d, autoResolved %d; want 1 open, 0 resolved", result.conflicts, result.autoResolved)
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open conflicts = %d, want 1", len(open))
	}
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

// TestEnginePushServerWinsKeepsDirtyWhenServerBodyEmpty reproduces issue #495.
// On a 412 with ConflictServerWins, if the re-fetched server body carries no
// importable VEVENT/VTODO/VJOURNAL, importICal applies nothing. The auto-resolve
// must not clear dirty or stamp the server ETag. That would drop the local
// edit behind a server version that was never adopted. The manual
// ResolveConflict path already guards against that asymmetry (#466). With the
// previous unconditional clear this test fails. Dirty is wiped. The ETag is
// advanced with nothing applied.
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
	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins, false)
	if err != nil {
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

	// The conflict row is now recorded and left open, so the user can resolve
	// the divergence by hand instead of it vanishing (issue #610).
	if result.conflicts != 1 || result.autoResolved != 0 {
		t.Fatalf("result = conflicts %d, autoResolved %d; want 1 open, 0 resolved", result.conflicts, result.autoResolved)
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(open) != 1 || open[0].Uid != "srv-wins-empty" {
		t.Fatalf("open conflicts = %+v, want the srv-wins-empty row", open)
	}
}

// TestPersistImportedPrunesStaleOverrides verifies that when a CalDAV server
// deletes a recurring instance, persistImported soft-deletes the stale local
// override row. A server signals instance deletion by a remove of the override
// VEVENT from the resource and an add of the slot to the master's EXDATE.
// Without a prune, the stale override is still CONFIRMED. Expansion then
// resurrects it. The orphan checker deliberately ignores EXDATEs so a
// legitimate override is never mistaken for an orphan.
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
	_, _, err = engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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
	_, _, err = engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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
// server has never seen it. A prune of it would discard the edit in silence.
// The prune must then skip dirty resources.
func TestPersistImportedPruneSkipsDirtyResource(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	testutil.LinkCalendarToAccount(t, db)
	calendarID := int64(1) // LinkCalendarToAccount links the seeded calendar 1

	const uid = "dirty-prune-test"
	const localRID = "2026-07-02T17:00:00Z"

	// First pull: master only, then the tracking row settles clean.
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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

	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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
// absent from the server. A non-zero SkippedComponents must then disable
// prune for the whole result.
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

	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
		Events: []event.Event{
			pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
			pruneTestEvent(uid, calendarID, rid, ""),
		},
	}); err != nil {
		t.Fatalf("first persistImported: %v", err)
	}

	// Second pull: the override VEVENT was dropped by the parser, not by the
	// server. The keep-set is an incomplete inventory — no pruning.
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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

// A resource that holds components of more than one UID must reconcile each
// UID against its own master. UID A's master says nothing about UID B's
// override inventory.
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
		if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{
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
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "pending-event", OwnerType: "event",
		RemoteUrl: "/calendar/pending-event.ics", Etag: `"old"`, Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID, Uid: "pending-delete", RemoteUrl: "/calendar/pending-delete.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}

	result, err := engine.SyncCalendar(ctx, calendarID, ConflictServerWins)
	if err != nil {
		t.Fatalf("SyncCalendar: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("read-only sync errors = %v", result.Errors)
	}
	tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListTombstonesByCalendar: %v", err)
	}
	if len(tombstones) != 1 || tombstones[0].Uid != "pending-delete" {
		t.Fatalf("tombstones = %+v, want pending delete unchanged", tombstones)
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

// PushLocalEdits is the opportunistic save-time fast path. A read-only
// calendar must short-circuit before any write phase. A save against a
// subscribed calendar then never reaches the server. That matches the
// SyncCalendar gate above.
func TestEnginePushLocalEditsReadOnlyIsNoOpWithoutServerContact(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("read-only PushLocalEdits must not contact the server: %s %s", r.Method, r.URL.Path)
		http.Error(w, "read-only push must be a no-op", http.StatusForbidden)
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
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "pending-event", OwnerType: "event",
		RemoteUrl: "/calendar/pending-event.ics", Etag: `"old"`, Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID, Uid: "pending-delete", RemoteUrl: "/calendar/pending-delete.ics",
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}

	result, err := engine.PushLocalEdits(ctx, calendarID)
	if err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	if result.Pushed != 0 || result.Deleted != 0 || len(result.Errors) != 0 {
		t.Fatalf("read-only push result = %+v, want an empty no-op", result)
	}
	resources, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(resources) != 1 || resources[0].Uid != "pending-event" {
		t.Fatalf("dirty resources = %+v, want pending event unchanged", resources)
	}
	tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListTombstonesByCalendar: %v", err)
	}
	if len(tombstones) != 1 || tombstones[0].Uid != "pending-delete" {
		t.Fatalf("tombstones = %+v, want pending delete unchanged", tombstones)
	}
}

func TestEngineSyncCalendarSerializesWholeCycle(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	overlap := make(chan struct{}, 1)
	var requestsMu gosync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests++
		requestNumber := requests
		requestsMu.Unlock()
		if requestNumber == 1 {
			close(firstEntered)
			<-releaseFirst
		} else {
			select {
			case overlap <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav"></d:multistatus>`)
	}))
	defer server.Close()

	remoteAccount, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "serialized", ServerUrl: server.URL, AuthType: "basic", Username: "user",
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

	results := make(chan error, 2)
	go func() {
		_, err := engine.SyncCalendar(ctx, calendarID, ConflictServerWins)
		results <- err
	}()
	<-firstEntered
	go func() {
		_, err := engine.SyncCalendar(ctx, calendarID, ConflictServerWins)
		results <- err
	}()

	select {
	case <-overlap:
		close(releaseFirst)
		t.Fatal("second sync reached the server before the first cycle completed")
	case <-time.After(100 * time.Millisecond):
		close(releaseFirst)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("SyncCalendar: %v", err)
		}
	}
}

func TestExportResourceFor_HydrateErrorAbortsExport(t *testing.T) {
	t.Parallel()

	get := func(context.Context, string) (event.Event, error) {
		return event.Event{UID: "uid-1", ID: 1}, nil
	}
	listOverrides := func(context.Context, string) ([]event.Event, error) {
		return nil, nil
	}
	marker := errors.New("db busy")
	hydrate := func(context.Context, *Engine, *event.Event) ([]string, error) {
		return nil, marker
	}
	exportCalled := false
	hydrated := func([]event.Event, string) ([]byte, error) {
		exportCalled = true
		return []byte("BEGIN:VCALENDAR"), nil
	}

	_, _, err := exportResourceFor(context.Background(), nil, "uid-1", "event",
		get, listOverrides, hydrate, hydrated)
	if err == nil {
		t.Fatal("expected hydration error to propagate, got nil")
	}
	if !errors.Is(err, marker) {
		t.Errorf("error = %v, want %v", err, marker)
	}
	if exportCalled {
		t.Error("export must not run when hydration fails: an amputated payload would overwrite the server resource")
	}
}

// A gone master row is expected. Google serves orphan instances under their
// own UID. A transient read failure is not. Treat the two alike and the
// exporter emitted the override rows alone. It PUT a resource with the master
// VEVENT and its recurrence rule amputated. It then cleared the dirty flag.
// Nothing retried.
func TestExportResourceFor_MasterReadErrorAbortsExport(t *testing.T) {
	t.Parallel()

	marker := errors.New("database is locked")
	get := func(context.Context, string) (event.Event, error) {
		return event.Event{}, marker
	}
	listOverrides := func(context.Context, string) ([]event.Event, error) {
		return []event.Event{{UID: "uid-1", ID: 2, RecurrenceID: "20260401T100000Z"}}, nil
	}
	hydrate := func(context.Context, *Engine, *event.Event) ([]string, error) { return nil, nil }
	exportCalled := false
	export := func([]event.Event, string) ([]byte, error) {
		exportCalled = true
		return []byte("BEGIN:VCALENDAR"), nil
	}

	_, _, err := exportResourceFor(context.Background(), nil, "uid-1", "event",
		get, listOverrides, hydrate, export)
	if err == nil {
		t.Fatal("expected the master read error to propagate, got nil")
	}
	if !errors.Is(err, marker) {
		t.Errorf("error = %v, want %v", err, marker)
	}
	if exportCalled {
		t.Error("export must not run: a PUT built from overrides alone deletes the master from the server")
	}
}

// The orphan-instance path must still succeed. sql.ErrNoRows on the master is a
// legitimate shape, not a failure.
func TestExportResourceFor_MissingMasterExportsOverrides(t *testing.T) {
	t.Parallel()

	get := func(context.Context, string) (event.Event, error) {
		return event.Event{}, sql.ErrNoRows
	}
	listOverrides := func(context.Context, string) ([]event.Event, error) {
		return []event.Event{{UID: "uid-1", ID: 2, RecurrenceID: "20260401T100000Z"}}, nil
	}
	hydrate := func(context.Context, *Engine, *event.Event) ([]string, error) { return nil, nil }
	var exported int
	export := func(rows []event.Event, _ string) ([]byte, error) {
		exported = len(rows)
		return []byte("BEGIN:VCALENDAR"), nil
	}

	if _, _, err := exportResourceFor(context.Background(), nil, "uid-1", "event",
		get, listOverrides, hydrate, export); err != nil {
		t.Fatalf("orphan instance must still export: %v", err)
	}
	if exported != 1 {
		t.Errorf("exported %d rows, want 1", exported)
	}
}

// NewEngine with a nil logger must not fall back to slog.Default. TUI
// callers pass nil while Bubble Tea owns the terminal. Any write to
// the default (stderr) handler would then print over the display.
func TestNewEngine_NilLoggerIsSilent(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil)
	e.logger.Info("sync started")

	if buf.Len() > 0 {
		t.Fatalf("nil-logger engine wrote to slog.Default (stderr in production, which corrupts the TUI): %q", buf.String())
	}
}

// TestPersistImportedDropsInvalidAlarm covers issue #585. A server
// resource that carries one alarm the write rule rejects must still
// persist: the event and its valid alarms land, the drop travels back as
// a warning, and the pull does not fail. The old code propagated the
// error, so the whole resource stayed absent and retried on every cycle
// while the report showed nothing.
func TestPersistImportedDropsInvalidAlarm(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	start := time.Date(2026, 6, 18, 17, 0, 0, 0, time.UTC)
	// The iCal parser sanitizes an alarm, so the bad value is built here
	// the way a future producer would deliver it.
	result := icalPkg.ImportResult{
		Events: []event.Event{{
			UID:        "bad-alarm-uid",
			CalendarID: calendarID,
			Title:      "Carries one bad alarm",
			StartTime:  start,
			EndTime:    start.Add(time.Hour),
			Alarms: []model.Alarm{
				{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "START"},
				{Action: "DISPLAY", TriggerValue: "-PT30M", Related: "MIDDLE"},
			},
		}},
	}

	revs, warnings, err := engine.persistImported(ctx, calendarID, result)
	if err != nil {
		t.Fatalf("persistImported must not fail the resource for one bad alarm: %v", err)
	}
	if _, ok := revs["bad-alarm-uid"]; !ok {
		t.Fatalf("revs = %v, want an entry for the persisted event", revs)
	}

	saved, err := q.GetEventByUID(ctx, "bad-alarm-uid")
	if err != nil {
		t.Fatalf("GetEventByUID: the event must persist: %v", err)
	}
	alarms, err := engine.events.ListAlarms(ctx, saved.ID)
	if err != nil {
		t.Fatalf("ListAlarms: %v", err)
	}
	if len(alarms) != 1 {
		t.Fatalf("alarms = %d, want 1 (the valid one); got %+v", len(alarms), alarms)
	}
	if alarms[0].TriggerValue != "-PT15M" {
		t.Errorf("kept alarm trigger = %q, want -PT15M", alarms[0].TriggerValue)
	}

	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1; got %v", len(warnings), warnings)
	}
	for _, want := range []string{"bad-alarm-uid", "alarm 2", "MIDDLE"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning %q does not name %q", warnings[0], want)
		}
	}
}
