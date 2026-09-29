package sync

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestEnginePullOpenConflictKeepsServerBodyWhenUnencodable covers a fetched
// body that the importer accepts but the strict go-ical encoder rejects. The
// open conflict row needs the body as text, so the pull keeps the older
// server copy and warns. The pull must not write an empty server body.
func TestEnginePullOpenConflictKeepsServerBodyWhenUnencodable(t *testing.T) {
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

	const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/open-conflict.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-server-v2&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/abc</d:sync-token>
</d:multistatus>`

	// No DTSTAMP: the importer accepts the VEVENT, the encoder rejects it.
	const fetchBody = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:open-conflict-uid
DTSTART:20260403T120000Z
DTEND:20260403T130000Z
SUMMARY:Server version v2
END:VEVENT
END:VCALENDAR
`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		body := syncBody
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
		t.Fatalf("pulled = %d, want 0 (open conflict)", result.pulled)
	}
	var warned bool
	for _, w := range result.warnings {
		if w.UID == uid && strings.Contains(w.Message, "keeps the older server copy") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("warnings = %v, want the older-server-copy warning", result.warnings)
	}

	open, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open conflicts = %d, want 1", len(open))
	}
	if open[0].ServerIcal != "server body" || open[0].ServerEtag != `"etag-server"` {
		t.Fatalf("conflict server copy = (%q, %q), want the recorded copy", open[0].ServerIcal, open[0].ServerEtag)
	}
}
