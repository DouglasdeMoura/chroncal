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
// pulled=0 errors=0). The pull surfaces a warning on every attempt and a
// final warning when it gives up refetching the href.
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

	// Attempts 2 and 3: the pending-href retry refetches the body. Each
	// attempt warns.
	for attempt := 2; attempt <= 3; attempt++ {
		result, err = engine.pull(ctx, client, calendarID, "/calendar/")
		if err != nil {
			t.Fatalf("pull %d: %v", attempt, err)
		}
		if !hasWarningWith(result.warnings, "/calendar/garbage.ics", "unparseable") {
			t.Fatalf("pull %d: warnings = %v, want an unparseable warning", attempt, result.warnings)
		}
	}

	// Attempt 3 exhausts the miss budget, so the engine drops the retry
	// obligation and says so. The third pull's warnings carry the give-up
	// line alongside the unparseable line.
	gaveUp := false
	for _, w := range result.warnings {
		if w.Path == "/calendar/garbage.ics" && strings.Contains(w.Message, "gave up") {
			gaveUp = true
		}
	}
	if !gaveUp {
		t.Fatalf("pull 3: warnings = %v, want a gave-up warning for /calendar/garbage.ics", result.warnings)
	}

	// The pending href is gone, so a fourth pull fetches nothing and stops
	// warning. The give-up line fired once; the story ends there.
	result, err = engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull 4: %v", err)
	}
	for _, w := range result.warnings {
		if strings.Contains(w.Message, "garbage") {
			t.Fatalf("pull 4: unexpected warning %v after give-up", w)
		}
	}
}

func hasWarningWith(warnings []ImportWarning, path, substr string) bool {
	for _, w := range warnings {
		if w.Path == path && strings.Contains(w.Message, substr) {
			return true
		}
	}
	return false
}
