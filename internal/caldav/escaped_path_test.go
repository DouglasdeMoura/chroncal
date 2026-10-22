package caldav

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// googleHolidayHref is the collection href of the Google public holiday
// calendar. The "%23" is an escaped "#" in the path.
const googleHolidayHref = "/caldav/v2/en.german%23holiday@group.v.calendar.google.com/events/"

// TestClientRequestsKeepEscapedHash covers issue #860. The canonical refs
// returned decoded paths. A request URL that parsed such a path again cut
// the path at the "#", and Google answered the shortened URL with HTTP 405.
func TestClientRequestsKeepEscapedHash(t *testing.T) {
	t.Parallel()

	const objectHref = googleHolidayHref + "a%23b.ics"
	cases := []struct {
		name     string
		wantPath string
		call     func(*Client) error
	}{
		{"QueryAll", googleHolidayHref, func(c *Client) error {
			_, err := c.QueryAll(context.Background(), googleHolidayHref)
			return err
		}},
		{"SyncCollection", googleHolidayHref, func(c *Client) error {
			_, err := c.SyncCollection(context.Background(), googleHolidayHref, "")
			return err
		}},
		{"MultiGetTolerant", googleHolidayHref, func(c *Client) error {
			path, err := c.CanonicalObjectRef(googleHolidayHref, objectHref)
			if err != nil {
				return err
			}
			_, err = c.MultiGetTolerant(context.Background(), googleHolidayHref, []string{path})
			return err
		}},
		{"PutResource", objectHref, func(c *Client) error {
			path, err := c.CanonicalObjectRef(googleHolidayHref, objectHref)
			if err != nil {
				return err
			}
			_, err = c.PutResource(context.Background(), path, testCalendar(t), "")
			return err
		}},
		{"DeleteResource", objectHref, func(c *Client) error {
			path, err := c.CanonicalObjectRef(googleHolidayHref, objectHref)
			if err != nil {
				return err
			}
			return c.DeleteResource(context.Background(), path, "")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotPath, gotFragment, gotBody string
			client, err := NewClient(putTestHTTPClient{do: func(req *http.Request) (*http.Response, error) {
				gotPath = req.URL.EscapedPath()
				gotFragment = req.URL.Fragment
				if req.Body != nil {
					raw, _ := io.ReadAll(req.Body)
					gotBody = string(raw)
				}
				resp := &http.Response{
					StatusCode: http.StatusMultiStatus,
					Status:     "207 Multi-Status",
					Header:     http.Header{"Content-Type": []string{"application/xml"}},
					Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:"><d:sync-token>tok</d:sync-token></d:multistatus>`)),
					Request: req,
				}
				switch req.Method {
				case http.MethodPut:
					resp.StatusCode, resp.Status = http.StatusCreated, "201 Created"
					resp.Header.Set("ETag", `"etag-new"`)
				case http.MethodDelete:
					resp.StatusCode, resp.Status = http.StatusNoContent, "204 No Content"
				}
				return resp, nil
			}}, "https://apidata.googleusercontent.com/caldav/v2/")
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			if err := tc.call(client); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if gotPath != tc.wantPath || gotFragment != "" {
				t.Fatalf("request path = %q fragment = %q, want path %q and no fragment", gotPath, gotFragment, tc.wantPath)
			}
			if tc.name == "MultiGetTolerant" && !strings.Contains(gotBody, "<d:href>"+objectHref+"</d:href>") {
				t.Fatalf("multiget body does not carry the escaped href %q:\n%s", objectHref, gotBody)
			}
		})
	}
}

// TestCanonicalObjectRefEscapeForms pins one spelling per path. An escape
// of '/', '?', '#', or '%' stays, so the path keeps its structure. Every
// other escape decodes, so Google's "%40" matches the "@" that an older
// version stored in RemoteUrl (issue #860).
func TestCanonicalObjectRefEscapeForms(t *testing.T) {
	t.Parallel()

	client, err := NewClient(putTestHTTPClient{}, "https://apidata.googleusercontent.com/caldav/v2/")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cases := []struct{ ref, want string }{
		{"/cal/x%40virtual/events/a%40google.com.ics", "/cal/x@virtual/events/a@google.com.ics"},
		{"/cal/x@virtual/events/a@google.com.ics", "/cal/x@virtual/events/a@google.com.ics"},
		{"/cal/a%23b%3fc.ics", "/cal/a%23b%3Fc.ics"},
		{"/cal/..%2F..%2fescape.ics", "/cal/..%2F..%2Fescape.ics"},
		{"/cal/p%25q%20r.ics", "/cal/p%25q%20r.ics"},
		{"/cal/caf%C3%A9.ics", "/cal/caf%C3%A9.ics"},
		{"/cal/a%7Eb.ics", "/cal/a~b.ics"},
	}
	for _, tc := range cases {
		got, err := client.CanonicalObjectRef("/cal/", tc.ref)
		if err != nil {
			t.Fatalf("CanonicalObjectRef(%q): %v", tc.ref, err)
		}
		if got != tc.want {
			t.Errorf("CanonicalObjectRef(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}

	got, err := client.CanonicalCollectionRef(googleHolidayHref)
	if err != nil {
		t.Fatalf("CanonicalCollectionRef: %v", err)
	}
	if got != googleHolidayHref {
		t.Errorf("CanonicalCollectionRef = %q, want %q", got, googleHolidayHref)
	}
}
