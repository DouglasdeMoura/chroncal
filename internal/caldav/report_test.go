package caldav

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestClientQueryAllSurvivesBadBodies covers issue #853: one response
// without calendar-data, or with a body the ical decoder rejects, must
// not fail the whole calendar-query. The good resource still arrives, and
// each bad href lands in Missing or Unparseable with its ETag.
func TestClientQueryAllSurvivesBadBodies(t *testing.T) {
	t.Parallel()

	const responseBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/good.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-good&quot;</d:getetag>
        <cal:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:good-uid
DTSTAMP:20260606T120000Z
DTSTART:20260606T120000Z
DTEND:20260606T130000Z
SUMMARY:Good
END:VEVENT
END:VCALENDAR
</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
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
  <d:response>
    <d:href>/calendar/nodata.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-nodata&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
    <d:propstat>
      <d:prop>
        <cal:calendar-data/>
      </d:prop>
      <d:status>HTTP/1.1 404 Not Found</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/calendar/gone.ics</d:href>
    <d:status>HTTP/1.1 404 Not Found</d:status>
  </d:response>
</d:multistatus>`

	client, err := NewClient(putTestHTTPClient{do: func(req *http.Request) (*http.Response, error) {
		if req.Method != "REPORT" {
			t.Errorf("method = %s, want REPORT", req.Method)
		}
		if depth := req.Header.Get("Depth"); depth != "1" {
			t.Errorf("Depth = %q, want 1", depth)
		}
		raw, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(raw), "calendar-query") {
			t.Errorf("request body is not a calendar-query: %s", raw)
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    req,
		}, nil
	}}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	result, err := client.QueryAll(context.Background(), "/calendar/")
	if err != nil {
		t.Fatalf("QueryAll: %v", err)
	}
	if len(result.Resources) != 1 || result.Resources[0].Path != "/calendar/good.ics" ||
		result.Resources[0].ETag != "etag-good" || result.Resources[0].Data == nil {
		t.Fatalf("Resources = %+v, want the one parsed body of /calendar/good.ics", result.Resources)
	}
	wantUnparseable := []ReportMiss{{Href: "/calendar/garbage.ics", ETag: "etag-garbage"}}
	if len(result.Unparseable) != 1 || result.Unparseable[0] != wantUnparseable[0] {
		t.Fatalf("Unparseable = %+v, want %+v", result.Unparseable, wantUnparseable)
	}
	wantMissing := []ReportMiss{
		{Href: "/calendar/nodata.ics", ETag: "etag-nodata"},
		{Href: "/calendar/gone.ics"},
	}
	if len(result.Missing) != 2 || result.Missing[0] != wantMissing[0] || result.Missing[1] != wantMissing[1] {
		t.Fatalf("Missing = %+v, want %+v", result.Missing, wantMissing)
	}
}

// TestClientQueryAllFailsOnReportError keeps request-level failures fatal:
// a non-207 status is an error, not an empty snapshot.
func TestClientQueryAllFailsOnReportError(t *testing.T) {
	t.Parallel()

	client, err := NewClient(putTestHTTPClient{do: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("boom")),
			Request:    req,
		}, nil
	}}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.QueryAll(context.Background(), "/calendar/"); err == nil {
		t.Fatal("QueryAll err = nil, want a report-level failure")
	}
}

// TestDecodeTolerantReportMergesSplitPropstats covers RFC 4918 section
// 9.1.2: a server may put getetag and calendar-data in two separate 200
// propstats. The decoder must merge them into one Resource, not report
// the href as Missing.
func TestDecodeTolerantReportMergesSplitPropstats(t *testing.T) {
	t.Parallel()

	const responseBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/split.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-split&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
    <d:propstat>
      <d:prop>
        <cal:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:split-uid
DTSTAMP:20260606T120000Z
DTSTART:20260606T120000Z
DTEND:20260606T130000Z
SUMMARY:Split
END:VEVENT
END:VCALENDAR
</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

	result, err := decodeTolerantReport(strings.NewReader(responseBody))
	if err != nil {
		t.Fatalf("decodeTolerantReport: %v", err)
	}
	if len(result.Missing) != 0 || len(result.Unparseable) != 0 {
		t.Fatalf("Missing = %+v, Unparseable = %+v, want none", result.Missing, result.Unparseable)
	}
	if len(result.Resources) != 1 || result.Resources[0].Path != "/calendar/split.ics" ||
		result.Resources[0].ETag != "etag-split" || result.Resources[0].Data == nil {
		t.Fatalf("Resources = %+v, want one parsed body of /calendar/split.ics with ETag etag-split", result.Resources)
	}
}
