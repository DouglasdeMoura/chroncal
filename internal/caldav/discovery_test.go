package caldav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverCalendarsReturnsCollectionMetadata(t *testing.T) {
	var homeSetDepth, homeSetBody string
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)

		var body string
		switch r.URL.Path {
		case "/":
			body = multistatus(`/`, `<d:current-user-principal><d:href>/principals/me/</d:href></d:current-user-principal>`)
		case "/principals/me/":
			body = multistatus(`/principals/me/`, `<c:calendar-home-set><d:href>/calendars/me/</d:href></c:calendar-home-set>`)
		case "/calendars/me/":
			// Metadata — color, ACL, components — is inlined in the single
			// Depth:1 home-set response; no per-calendar PROPFIND follows.
			homeSetDepth = r.Header.Get("Depth")
			raw, _ := io.ReadAll(r.Body)
			homeSetBody = string(raw)
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/calendars/me/</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:response>
    <d:href>/calendars/me/work/</d:href>
    <d:propstat><d:prop>
      <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
      <d:displayname>Work</d:displayname>
      <c:calendar-description>Team schedule</c:calendar-description>
      <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
      <d:current-user-privilege-set><d:privilege><d:write/></d:privilege></d:current-user-privilege-set>
      <ic:calendar-color>#123456FF</ic:calendar-color>
    </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:response>
    <d:href>/calendars/me/holidays/</d:href>
    <d:propstat><d:prop>
      <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
      <d:displayname>Holidays in Brazil</d:displayname>
      <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
      <d:current-user-privilege-set><d:privilege><d:read/></d:privilege></d:current-user-privilege-set>
      <ic:calendar-color>#ABCDEF</ic:calendar-color>
    </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
</d:multistatus>`
		default:
			t.Errorf("unexpected discovery request path %q", r.URL.Path)
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
		t.Fatalf("calendar count = %d, want 2 (home-set self-response must be filtered out)", len(found))
	}

	work := found[0]
	if work.Path != "/calendars/me/work/" {
		t.Errorf("work path = %q, want canonical /calendars/me/work/", work.Path)
	}
	if work.Color != "#123456" {
		t.Errorf("work color = %q, want #123456 (alpha byte stripped in one batch)", work.Color)
	}
	if work.Access != CalendarAccessWrite {
		t.Errorf("work access = %q, want write", work.Access)
	}
	if work.Description != "Team schedule" {
		t.Errorf("work description = %q", work.Description)
	}
	if len(work.SupportedComponentSet) != 1 || work.SupportedComponentSet[0] != "VEVENT" {
		t.Errorf("work components = %v, want [VEVENT]", work.SupportedComponentSet)
	}

	holidays := found[1]
	if holidays.Path != "/calendars/me/holidays/" || holidays.Color != "#ABCDEF" || holidays.Access != CalendarAccessRead {
		t.Errorf("holiday calendar = %+v, want canonical path, color, and read access", holidays)
	}

	// Metadata must come from the single Depth:1 home-set PROPFIND — never a
	// per-calendar Depth:0 PROPFIND (the N+1 round trip this replaces).
	for _, p := range requested {
		if p == "/calendars/me/work/" || p == "/calendars/me/holidays/" {
			t.Fatalf("discovery issued a per-calendar PROPFIND to %q; metadata must come from the home-set batch", p)
		}
	}
	if homeSetDepth != "1" {
		t.Errorf("home-set PROPFIND Depth = %q, want 1", homeSetDepth)
	}
	if !strings.Contains(homeSetBody, "<ic:calendar-color/>") {
		t.Errorf("home-set PROPFIND must request ic:calendar-color in one batch, got: %s", homeSetBody)
	}
	if !strings.Contains(homeSetBody, "<d:current-user-privilege-set/>") {
		t.Errorf("home-set PROPFIND must request current-user-privilege-set in one batch, got: %s", homeSetBody)
	}
}

func multistatus(href, prop string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop>
  <d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`, href, prop)
}
