package sync

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestEnginePullInitialSnapshotKeepsRowWhenImportFails covers a changed body
// that does not import (here: a VEVENT without UID). The import then returns
// no UID. The initial snapshot must still count the local row of that path as
// seen. Otherwise the absence rule deletes a healthy local event.
func TestEnginePullInitialSnapshotKeepsRowWhenImportFails(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "kept")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "kept", OwnerType: "event",
		RemoteUrl: "/calendar/kept.ics", Etag: "old", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/calendar/kept.ics</d:href>
    <d:propstat><d:prop><d:getetag>&quot;new&quot;</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/t1</d:sync-token>
</d:multistatus>`

	const noUIDBody = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
SUMMARY:No UID
END:VEVENT
END:VCALENDAR
`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		body := syncBody
		if strings.Contains(string(raw), "calendar-multiget") {
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/kept.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;new&quot;</d:getetag>
        <cal:calendar-data>` + noUIDBody + `</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
		}
		return &http.Response{StatusCode: http.StatusMultiStatus, Status: "207 Multi-Status",
			Header: http.Header{"Content-Type": []string{"application/xml"}},
			Body:   io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0", result.deleted)
	}
	if _, err := q.GetEventByUID(ctx, "kept"); err != nil {
		t.Fatalf("local row was deleted after a failed import: %v", err)
	}
}
