package sync

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestEnginePullWarnsOnUnparseableResource verifies that a resource the
// server holds but chroncal cannot parse no longer vanishes in silence
// (issue #805: an entry sits unimported while sync run reports
// pulled=0 errors=0). The pull gives a warning on each attempt. The warning
// of the last attempt also tells that the pull stops the retries.
func TestEnginePullWarnsOnUnparseableResource(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/garbage.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-garbage&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/garbage-1</d:sync-token>
</d:multistatus>`

	const emptySyncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:sync-token>https://example.com/sync/garbage-2</d:sync-token>
</d:multistatus>`

	multigetCount := 0
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
			body := emptySyncBody
			if multigetCount == 0 {
				body = syncBody
			}
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Status:     "207 Multi-Status",
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}
		multigetCount++
		multigetBody := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/garbage.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-garbage&quot;</d:getetag>
        <cal:calendar-data>this is not an ical body at all</cal:calendar-data>
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

	// Attempt 1: the change list nominates the href. The body does not
	// parse. The pull must warn, not stay silent.
	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull 1: %v", err)
	}
	if result.pulled != 0 {
		t.Fatalf("pull 1: pulled = %d, want 0", result.pulled)
	}
	if !hasWarningWith(result.warnings, "/calendar/garbage.ics", "unparseable") {
		t.Fatalf("pull 1: warnings = %v, want an unparseable warning for /calendar/garbage.ics", result.warnings)
	}

	// The pending-href retry fetches the body again on each later pull.
	// Each attempt warns once. The last attempt exhausts the miss budget,
	// so its one warning also tells the user that the retries stop.
	for attempt := 2; attempt <= pendingHrefMissLimit; attempt++ {
		result, err = engine.pull(ctx, client, calendarID, "/calendar/")
		if err != nil {
			t.Fatalf("pull %d: %v", attempt, err)
		}
		if !hasWarningWith(result.warnings, "/calendar/garbage.ics", "unparseable") {
			t.Fatalf("pull %d: warnings = %v, want an unparseable warning", attempt, result.warnings)
		}
		if n := countWarningsFor(result.warnings, "/calendar/garbage.ics"); n != 1 {
			t.Fatalf("pull %d: %d warnings for the href, want 1: %v", attempt, n, result.warnings)
		}
	}
	if !hasWarningWith(result.warnings, "/calendar/garbage.ics", "gave up") {
		t.Fatalf("pull %d: warnings = %v, want a gave-up warning for /calendar/garbage.ics", pendingHrefMissLimit, result.warnings)
	}

	// The pending href is gone. The next pull fetches nothing and does not
	// warn about the href again.
	result, err = engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull after give-up: %v", err)
	}
	if n := countWarningsFor(result.warnings, "/calendar/garbage.ics"); n != 0 {
		t.Fatalf("pull after give-up: unexpected warnings %v", result.warnings)
	}
}

func countWarningsFor(warnings []ImportWarning, path string) int {
	n := 0
	for _, w := range warnings {
		if w.Path == path {
			n++
		}
	}
	return n
}

func hasWarningWith(warnings []ImportWarning, path, substr string) bool {
	for _, w := range warnings {
		if w.Path == path && strings.Contains(w.Message, substr) {
			return true
		}
	}
	return false
}
