package caldav

import (
	"context"
	"encoding/xml"
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
        <d:current-user-privilege-set>
          <d:privilege><d:read/></d:privilege>
          <d:privilege><d:write/></d:privilege>
        </d:current-user-privilege-set>
        <c:supported-calendar-component-set>
          <c:comp name="VEVENT"/>
          <c:comp name="VTODO"/>
        </c:supported-calendar-component-set>
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
	if meta.Access != CalendarAccessWrite {
		t.Errorf("Access = %q, want %q", meta.Access, CalendarAccessWrite)
	}
	if got := strings.Join(meta.SupportedComponents, ","); got != "VEVENT,VTODO" {
		t.Errorf("SupportedComponents = %q, want VEVENT,VTODO", got)
	}
	if !strings.Contains(bodySeen, "<ic:calendar-color/>") {
		t.Errorf("PROPFIND body must request ic:calendar-color so the UI can adopt the server color at link time, got: %s", bodySeen)
	}
	if !strings.Contains(bodySeen, "<d:current-user-privilege-set/>") {
		t.Errorf("PROPFIND body must request current-user-privilege-set so read-only calendars can be disabled, got: %s", bodySeen)
	}
	if !strings.Contains(bodySeen, "<c:supported-calendar-component-set/>") {
		t.Errorf("PROPFIND body must request supported-calendar-component-set, got: %s", bodySeen)
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

func priv(names ...string) verifyCurrentUserPrivilegeSet {
	set := verifyCurrentUserPrivilegeSet{}
	for _, n := range names {
		set.Privileges = append(set.Privileges, verifyPrivilege{
			Names: []verifyPrivilegeName{{XMLName: xml.Name{Space: "DAV:", Local: n}}},
		})
	}
	return set
}

// Calendar access must be classified conservatively: aggregate privileges
// (all/write/owner) imply full write, granular rights require every operation
// chroncal sends, and anything short of that is read-only when readable so we
// never attempt an unsupported write.
func TestCalendarAccessFromPrivileges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		set  verifyCurrentUserPrivilegeSet
		want CalendarAccess
	}{
		{"empty", priv(), CalendarAccessUnknown},
		{"aggregate all", priv("all"), CalendarAccessWrite},
		{"aggregate write", priv("write"), CalendarAccessWrite},
		{"aggregate owner", priv("owner"), CalendarAccessWrite},
		{"all granular rights", priv("write-properties", "write-content", "bind", "unbind"), CalendarAccessWrite},
		{"granular missing bind", priv("write-properties", "write-content", "unbind", "read"), CalendarAccessRead},
		{"granular missing unbind", priv("write-properties", "write-content", "bind", "read"), CalendarAccessRead},
		{"granular missing write-content", priv("write-properties", "bind", "unbind", "read"), CalendarAccessRead},
		{"granular missing write-properties", priv("write-content", "bind", "unbind", "read"), CalendarAccessRead},
		{"granular without read", priv("write-properties", "write-content"), CalendarAccessUnknown},
		{"read only", priv("read"), CalendarAccessRead},
		{"read plus partial write stays read-only", priv("read", "write-content", "bind"), CalendarAccessRead},
		{"non-DAV namespace ignored", verifyCurrentUserPrivilegeSet{Privileges: []verifyPrivilege{{Names: []verifyPrivilegeName{{XMLName: xml.Name{Space: "urn:foo", Local: "write"}}}}}}, CalendarAccessUnknown},
	}

	for _, tc := range tests {
		if got := calendarAccessFromPrivileges(tc.set); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
