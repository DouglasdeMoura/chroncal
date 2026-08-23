package caldav

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TestDavMultiStatusDecodesCanonicalSample pins the shared envelope against a
// canonical multistatus body: one 404 propstat, then a 200 propstat. The
// decoder must keep both and firstOKPropstat must select the 200 one.
func TestDavMultiStatusDecodesCanonicalSample(t *testing.T) {
	body := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/cal/one/</d:href>
    <d:propstat>
      <d:status>HTTP/1.1 404 Not Found</d:status>
      <d:prop><d:getetag/></d:prop>
    </d:propstat>
    <d:propstat>
      <d:status>HTTP/1.1 200 OK</d:status>
      <d:prop>
        <d:getetag>"abc"</d:getetag>
        <ic:calendar-color>#FF0000AA</ic:calendar-color>
      </d:prop>
    </d:propstat>
  </d:response>
</d:multistatus>`

	var ms davMultiStatus[multiGetProps]
	if err := xml.NewDecoder(strings.NewReader(body)).Decode(&ms); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(ms.Responses))
	}
	r := ms.Responses[0]
	if r.Href != "/cal/one/" {
		t.Errorf("href = %q, want /cal/one/", r.Href)
	}
	if len(r.PropStats) != 2 {
		t.Fatalf("propstats = %d, want 2 (the envelope must keep non-2xx propstats)", len(r.PropStats))
	}

	ps, ok := firstOKPropstat(r)
	if !ok {
		t.Fatal("firstOKPropstat found no 2xx propstat")
	}
	if ps.ETag != `"abc"` {
		t.Errorf("etag = %q, want %q", ps.ETag, `"abc"`)
	}

	// A response with only error propstats yields ok == false, not an error.
	var ms2 davMultiStatus[multiGetProps]
	bad := strings.Replace(body, "200 OK", "500 Server Error", 1)
	bad = strings.Replace(bad, "404 Not Found", "500 Server Error", 1)
	if err := xml.NewDecoder(strings.NewReader(bad)).Decode(&ms2); err != nil {
		t.Fatalf("decode bad: %v", err)
	}
	if _, ok := firstOKPropstat(ms2.Responses[0]); ok {
		t.Error("firstOKPropstat accepted a 500 propstat")
	}
}

// TestAllOKPropstatsReturnsEvery2xxPayload pins the helper that the discovery
// and verify paths use. RFC 4918 §9.1.2 lets a server split the properties of
// one response across several 2xx propstats. The helper must return every 2xx
// payload in document order and skip every non-2xx propstat.
func TestAllOKPropstatsReturnsEvery2xxPayload(t *testing.T) {
	const body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/cal/one/</d:href>
    <d:propstat>
      <d:status>HTTP/1.1 200 OK</d:status>
      <d:prop><d:getetag>"first"</d:getetag></d:prop>
    </d:propstat>
    <d:propstat>
      <d:status>HTTP/1.1 404 Not Found</d:status>
      <d:prop><d:getetag>"dropped"</d:getetag></d:prop>
    </d:propstat>
    <d:propstat>
      <d:status>HTTP/1.1 200 OK</d:status>
      <d:prop><c:calendar-data>BEGIN:VCALENDAR
END:VCALENDAR</c:calendar-data></d:prop>
    </d:propstat>
  </d:response>
</d:multistatus>`

	var ms davMultiStatus[multiGetProps]
	if err := xml.NewDecoder(strings.NewReader(body)).Decode(&ms); err != nil {
		t.Fatalf("decode: %v", err)
	}
	props := allOKPropstats(ms.Responses[0])
	if len(props) != 2 {
		t.Fatalf("2xx payloads = %d, want 2 (the 404 propstat must be skipped)", len(props))
	}
	if props[0].ETag != `"first"` || props[1].ETag != "" {
		t.Errorf("payload order = [%q, %q], want the 200 propstats in document order", props[0].ETag, props[1].ETag)
	}
	if !strings.Contains(props[1].CalendarData, "BEGIN:VCALENDAR") {
		t.Errorf("second payload calendar-data = %q, want the VCALENDAR text from the third propstat", props[1].CalendarData)
	}

	// A response with only error propstats yields an empty slice, not an error.
	bad := strings.ReplaceAll(body, "200 OK", "500 Server Error")
	var ms2 davMultiStatus[multiGetProps]
	if err := xml.NewDecoder(strings.NewReader(bad)).Decode(&ms2); err != nil {
		t.Fatalf("decode bad: %v", err)
	}
	if props := allOKPropstats(ms2.Responses[0]); len(props) != 0 {
		t.Errorf("allOKPropstats accepted a 500 propstat: %+v", props)
	}
}
