package sync

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// ImportFile reports what it could not represent — a malformed DTEND replaced
// by a fabricated span, an alarm dropped for an unusable trigger. The pull path
// threw those away, so a value the server still holds correctly could be
// replaced by our fabrication on the next push with nothing anywhere saying so.
// The user's only signal is the log, so the pull has to write it.
func TestEnginePullSurfacesImportWarnings(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	db, q := testutil.NewTestDB(t)
	engine := NewEngine(db, q, &mockCredStore{creds: make(map[int64]auth.Credential)},
		calendar.NewService(db, q), event.NewService(db, q), todo.NewService(db, q),
		journal.NewService(db, q), logger)

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const reportBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/warned.ics</d:href>
    <d:propstat>
      <d:prop><d:getetag>&quot;etag-1&quot;</d:getetag></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/abc</d:sync-token>
</d:multistatus>`

	const fetchBody = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:warned-uid
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:garbage
SUMMARY:Event with an unparseable DTEND
END:VEVENT
END:VCALENDAR
`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		body := reportBody
		if strings.Contains(string(raw), "calendar-multiget") {
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/warned.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-1&quot;</d:getetag>
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

	out := logBuf.String()
	if !strings.Contains(out, "DTEND") {
		t.Errorf("the import warning never reached the log, so the fabricated span "+
			"is invisible before it is pushed back over the server's value; log:\n%s", out)
	}
	if !strings.Contains(out, "warned-uid") && !strings.Contains(out, "warned.ics") {
		t.Errorf("the warning does not identify the resource it came from; log:\n%s", out)
	}
}
