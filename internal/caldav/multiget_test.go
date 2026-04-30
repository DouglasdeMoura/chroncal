package caldav

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func newMultiGetClient(t *testing.T, do func(*http.Request) (*http.Response, error)) *Client {
	t.Helper()
	client, err := NewClient(putTestHTTPClient{do: do}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestMultiGetTolerantSurvivesPerResource404(t *testing.T) {
	t.Parallel()

	const responseBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/alive.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-alive&quot;</d:getetag>
        <cal:calendar-data>BEGIN:VCALENDAR
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
</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/calendar/gone-toplevel.ics</d:href>
    <d:status>HTTP/1.1 404 Not Found</d:status>
  </d:response>
  <d:response>
    <d:href>/calendar/gone-propstat.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag/>
        <cal:calendar-data/>
      </d:prop>
      <d:status>HTTP/1.1 404 Not Found</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

	client := newMultiGetClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "REPORT" {
			t.Fatalf("method = %s, want REPORT", r.Method)
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    r,
		}, nil
	})

	result, err := client.MultiGetTolerant(context.Background(), "/calendar/", []string{
		"/calendar/alive.ics",
		"/calendar/gone-toplevel.ics",
		"/calendar/gone-propstat.ics",
	})
	if err != nil {
		t.Fatalf("MultiGetTolerant: %v", err)
	}
	if len(result.Resources) != 1 {
		t.Fatalf("Resources = %d, want 1", len(result.Resources))
	}
	if result.Resources[0].Path != "/calendar/alive.ics" {
		t.Fatalf("alive path = %q, want /calendar/alive.ics", result.Resources[0].Path)
	}
	if result.Resources[0].ETag != "etag-alive" {
		t.Fatalf("alive etag = %q, want etag-alive", result.Resources[0].ETag)
	}
	if len(result.Missing) != 2 {
		t.Fatalf("Missing = %v, want 2 entries", result.Missing)
	}
	gotMissing := map[string]bool{}
	for _, m := range result.Missing {
		gotMissing[m] = true
	}
	if !gotMissing["/calendar/gone-toplevel.ics"] {
		t.Errorf("expected /calendar/gone-toplevel.ics in Missing")
	}
	if !gotMissing["/calendar/gone-propstat.ics"] {
		t.Errorf("expected /calendar/gone-propstat.ics in Missing")
	}
}

func TestMultiGetTolerantEmptyHrefList(t *testing.T) {
	t.Parallel()

	client := newMultiGetClient(t, func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected HTTP %s — empty href list must short-circuit", r.Method)
		return nil, nil
	})

	result, err := client.MultiGetTolerant(context.Background(), "/calendar/", nil)
	if err != nil {
		t.Fatalf("MultiGetTolerant: %v", err)
	}
	if len(result.Resources) != 0 || len(result.Missing) != 0 {
		t.Fatalf("expected empty result, got resources=%d missing=%d", len(result.Resources), len(result.Missing))
	}
}

func TestMultiGetTolerantBuildBodyEscapesHref(t *testing.T) {
	t.Parallel()

	body := buildMultiGetBody([]string{`/calendar/<bad>&"path".ics`})
	if strings.Contains(body, "<bad>") || strings.Contains(body, `"path"`) {
		t.Fatalf("body did not escape href:\n%s", body)
	}
	if !strings.Contains(body, "&lt;bad&gt;") {
		t.Fatalf("body missing escaped href:\n%s", body)
	}
}
