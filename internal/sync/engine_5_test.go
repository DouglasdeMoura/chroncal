package sync

import (
	"context"
	"database/sql"
	"errors"

	"io"

	"net/http"

	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestEnginePullPaginatesTruncatedSyncCollection reproduces the Google
// initial-snapshot data loss. The server truncates the sync-collection
// response (RFC 6578 §3.6 — a 507 marker on the collection plus a
// continuation token). The engine must page until complete. It diffs local
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
// COMPLETE inventory. It must appear exactly once, not double-counted.
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
// behavior the chokepoint enforces. If even one body 404s on multiget during
// an initial snapshot, the inventory is incomplete. NO absence-inferred
// deletion runs that round, not just the missed path. A locally-tracked row
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
// (servers without RFC 6578 sync-collection, e.g. GMX). Its deletions now
// route through the pendingDeletions chokepoint. A sync-collection REPORT that
// returns "unsupported" makes pull() fall back to pullFullSnapshot. A local
// pushed row absent from the QueryAll result must be deleted. A
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

// TestPersistImportedClearsRemovedAlarms is a regression test for issue #65.
// A CalDAV pull that re-imports a stored UID whose server component no
// longer carries an alarm must clear the locally stored alarm. Before the
// fix, persistImported only replaced child collections when the server sent a
// non-empty list. Server-side removals were then dropped in silence. Stale
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
	if _, _, err := engine.persistImported(ctx, calendarID, withAlarmResult); err != nil {
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
	if _, _, err := engine.persistImported(ctx, calendarID, noAlarmResult); err != nil {
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

// TestEnginePullWithholdsTokenOnPersistFailure covers issue #103. A fetched
// resource is successfully multiget'd. It then fails to persist locally (a
// transient SQLite busy/lock or a child-replace error). The pull must NOT
// advance the sync-token. Otherwise the token moves past the failed change.
// The next REPORT never re-lists it. The server-side update is then lost
// from the local copy indefinitely. The resource's old etag and the calendar
// sync-token must both stay put. The next sync then re-lists and retries.
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
// atomic per resource. If any Replace* step fails after the event row and some
// of its child collections have already been written, the entire resource is
// rolled back. It is not left in a partial state.
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

	if _, _, err := engine.persistImported(ctx, calendarID, result); err == nil {
		t.Fatal("expected persistImported to fail when a Replace step errors")
	}

	// The whole resource must roll back: no partial event row left behind.
	if _, err := engine.events.GetByUID(ctx, uid); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected event %q to be absent after rollback, got err=%v", uid, err)
	}
}

// TestLookupOwnerIDUnknownTypeErrors guards the owner-type dispatch. An
// unrecognized owner-type string must fail loudly. It must not resolve to
// ID 0 in silence. That would mis-attribute a sync conflict record.
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
// ID. A UID that is gone surfaces the lookup error instead of 0.
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
// dispatch entry point reports an unknown type through the same error. A
// new component type then cannot be skipped by one site in silence.
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

// TestEnginePushServerWinsPreservesConcurrentEdit reproduces issue #417. A local
// edit that lands in the window between the accept-server import and the dirty
// clear must not be dropped in silence. The afterImportRevCapture hook simulates
// that edit. The rev-guarded clear must leave the resource dirty. The next
// push then sends it. With the previous unconditional clear this test fails
// because the edit's dirty flag is wiped. Serial (no t.Parallel) because it
// mutates the package-level hook.
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

	client := serverWinsConflictClient(t, "srv-wins-race", nil)
	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins, false)
	if err != nil {
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

	// The rev guard kept dirty, so the recorded conflict row must stay open.
	// Marking it resolved would strand the concurrent edit behind a row the
	// user believes is settled.
	if result.conflicts != 1 || result.autoResolved != 0 {
		t.Fatalf("result = conflicts %d, autoResolved %d; want 1 open, 0 resolved", result.conflicts, result.autoResolved)
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open conflicts = %d, want 1 (row stays open while dirty survives)", len(open))
	}
}

// TestEnginePushServerWinsPreservesConcurrentEditAfterPersist reproduces issue
// #494. A local edit that commits between the accept-server import's persist
// commit and the dirty clear must not be dropped in silence. The
// afterImportPersist hook fires in that window, after persistImported committed
// and before clearDirtyAfterImport. It bumps rev and re-marks dirty exactly as
// a real service-layer mutation would. persistImported now captures the
// post-import rev inside its own transaction. It does not re-read it after
// commit, where this edit's bump would be read and matched. The rev-guarded
// clear then leaves the resource dirty. With the old after-commit re-read this
// test fails. The clear reads the edit's bumped rev and wipes dirty. Serial
// (no t.Parallel) because it mutates the package-level hook.
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
