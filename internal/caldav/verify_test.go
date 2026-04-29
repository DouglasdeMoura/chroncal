package caldav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyCalendarURL_ReturnsDisplayNameAndColor(t *testing.T) {
	t.Parallel()

	const fixture = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/dav/calendars/work/</d:href>
    <d:propstat>
      <d:prop>
        <d:displayname>Work</d:displayname>
        <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
        <ic:calendar-color>#9FE1E7FF</ic:calendar-color>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

	var bodySeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		bodySeen = string(body)
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)

	meta, err := VerifyCalendarURL(context.Background(), srv.URL+"/dav/calendars/work/", "user", "pass", "basic", true)
	if err != nil {
		t.Fatalf("VerifyCalendarURL: %v", err)
	}
	if meta.DisplayName != "Work" {
		t.Errorf("DisplayName = %q, want Work", meta.DisplayName)
	}
	if meta.Color != "#9FE1E7" {
		t.Errorf("Color = %q, want #9FE1E7 (alpha must be stripped so lipgloss renders it)", meta.Color)
	}
	if !strings.Contains(bodySeen, "<ic:calendar-color/>") {
		t.Errorf("PROPFIND body must request ic:calendar-color so the UI can adopt the server color at link time, got: %s", bodySeen)
	}
}

func TestFetchCalendarMetadata_DoesNotRequireCalendarResourceType(t *testing.T) {
	t.Parallel()

	// Fetch the metadata even when the response advertises only displayname
	// + color (some CalDAV servers gate resourcetype behind a different
	// PROPFIND depth/scope). VerifyCalendarURL still rejects this; the
	// link-time helper does not.
	const fixture = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/cal/</d:href>
    <d:propstat>
      <d:prop>
        <d:displayname>Personal</d:displayname>
        <ic:calendar-color>#445566</ic:calendar-color>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)

	meta, err := FetchCalendarMetadata(context.Background(), srv.URL+"/cal/", "user", "pass", "basic", true)
	if err != nil {
		t.Fatalf("FetchCalendarMetadata: %v", err)
	}
	if meta.Color != "#445566" {
		t.Errorf("Color = %q, want #445566", meta.Color)
	}
	if meta.DisplayName != "Personal" {
		t.Errorf("DisplayName = %q, want Personal", meta.DisplayName)
	}
}
