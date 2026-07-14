package caldav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoverCalendarsReturnsCollectionMetadata(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)

		var body string
		switch r.URL.Path {
		case "/":
			body = multistatus(`/`, `<d:current-user-principal><d:href>/principals/me/</d:href></d:current-user-principal>`)
		case "/principals/me/":
			body = multistatus(`/principals/me/`, `<c:calendar-home-set><d:href>/calendars/me/</d:href></c:calendar-home-set>`)
		case "/calendars/me/":
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendars/me/work/</d:href>
    <d:propstat><d:prop>
      <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
      <d:displayname>Work</d:displayname>
      <c:calendar-description>Team schedule</c:calendar-description>
      <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
    </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:response>
    <d:href>/calendars/me/holidays/</d:href>
    <d:propstat><d:prop>
      <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
      <d:displayname>Holidays in Brazil</d:displayname>
      <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
    </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
</d:multistatus>`
		case "/calendars/me/work/":
			body = calendarMetadataFixture("Work", "#123456FF", "write")
		case "/calendars/me/holidays/":
			body = calendarMetadataFixture("Holidays in Brazil", "#ABCDEF", "read")
		default:
			t.Fatalf("unexpected discovery request path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(http.DefaultClient, srv.URL+"/")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	found, err := client.DiscoverCalendars(context.Background())
	if err != nil {
		t.Fatalf("DiscoverCalendars: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("calendar count = %d, want 2", len(found))
	}

	if got := found[0]; got.Path != "/calendars/me/work/" || got.Color != "#123456" || got.Access != CalendarAccessWrite {
		t.Errorf("work calendar = %+v, want canonical path, normalized color, and write access", got)
	}
	if got := found[1]; got.Path != "/calendars/me/holidays/" || got.Color != "#ABCDEF" || got.Access != CalendarAccessRead {
		t.Errorf("holiday calendar = %+v, want canonical path, color, and read access", got)
	}
}

func multistatus(href, prop string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop>
  <d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`, href, prop)
}

func calendarMetadataFixture(name, color, privilege string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ic="http://apple.com/ns/ical/">
  <d:response><d:href>/</d:href><d:propstat><d:prop>
    <d:displayname>%s</d:displayname>
    <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
    <d:current-user-privilege-set><d:privilege><d:%s/></d:privilege></d:current-user-privilege-set>
    <ic:calendar-color>%s</ic:calendar-color>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`, name, privilege, color)
}
