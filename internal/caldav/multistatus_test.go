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
