package sync

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestEnginePullImportsUndatedVJournal verifies that a VJOURNAL without
// DTSTART survives a pull. RFC 5545 makes DTSTART optional in VJOURNAL, and
// jtx Board treats an undated VJOURNAL as a note. Issue #805: such entries
// vanished during sync with pulled=0 and no warning.
func TestEnginePullImportsUndatedVJournal(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	_ = db

	const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/note.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-note&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/after-note</d:sync-token>
</d:multistatus>`

	const noteICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VJOURNAL
UID:note-uid
DTSTAMP:20260403T120000Z
SUMMARY:Undated note
DESCRIPTION:A note captured without DTSTART
END:VJOURNAL
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
    <d:href>/calendar/note.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-note&quot;</d:getetag>
        <cal:calendar-data>` + noteICS + `</cal:calendar-data>
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
	})

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.pulled != 1 {
		t.Fatalf("pulled = %d, want 1 (undated note)", result.pulled)
	}

	j, err := q.GetJournalByUID(ctx, "note-uid")
	if err != nil {
		t.Fatalf("undated VJOURNAL was not persisted: %v", err)
	}
	if j.Summary != "Undated note" {
		t.Fatalf("summary = %q, want %q", j.Summary, "Undated note")
	}
	if j.StartDate != nil && *j.StartDate != "" {
		t.Fatalf("start_date = %q, want NULL (DTSTART was absent)", *j.StartDate)
	}
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "note-uid"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.OwnerType != "journal" {
		t.Fatalf("owner_type = %q, want journal", res.OwnerType)
	}
}
