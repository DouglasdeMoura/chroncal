package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

type putTestHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (c putTestHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.do(req)
}

func testCalendar(t *testing.T) *ical.Calendar {
	t.Helper()

	cal, err := ical.NewDecoder(strings.NewReader(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:test-event
DTSTAMP:20260403T100000Z
DTSTART:20260403T100000Z
DTEND:20260403T110000Z
SUMMARY:Test event
END:VEVENT
END:VCALENDAR
`)).Decode()
	if err != nil {
		t.Fatalf("Decode calendar: %v", err)
	}
	return cal
}

func TestClientPutResourceSendsIfMatch(t *testing.T) {
	t.Parallel()

	client, err := NewClient(putTestHTTPClient{do: func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", req.Method)
		}
		if req.URL.String() != "https://example.com/cal/test.ics" {
			t.Fatalf("url = %s, want https://example.com/cal/test.ics", req.URL.String())
		}
		if got := req.Header.Get("If-Match"); got != `"etag-before"` {
			t.Fatalf("If-Match = %q, want %q", got, `"etag-before"`)
		}
		if got := req.Header.Get("If-None-Match"); got != "" {
			t.Fatalf("If-None-Match = %q, want empty", got)
		}
		if got := req.Header.Get("Content-Type"); got != ical.MIMEType {
			t.Fatalf("Content-Type = %q, want %q", got, ical.MIMEType)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !strings.Contains(string(body), "UID:test-event") {
			t.Fatalf("PUT body missing UID:test-event:\n%s", string(body))
		}

		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     http.Header{"Etag": []string{`"etag-after"`}},
			Body:       io.NopCloser(http.NoBody),
			Request:    req,
		}, nil
	}}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	etag, err := client.PutResource(context.Background(), "/cal/test.ics", testCalendar(t), `"etag-before"`)
	if err != nil {
		t.Fatalf("PutResource: %v", err)
	}
	if etag != "etag-after" {
		t.Fatalf("etag = %q, want %q", etag, "etag-after")
	}
}

func TestNormalizeAndFormatIfMatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		raw           string
		wantNorm, ifM string
	}{
		{"strong", `"abc"`, "abc", `"abc"`},
		{"strong_unquoted", "abc", "abc", `"abc"`},
		{"strong_spaced", ` "abc" `, "abc", `"abc"`},
		// Weak validators must keep their W/ marker through normalization and
		// must NOT be asserted as strong validators in If-Match (RFC 7232 §3.1).
		{"weak", `W/"abc"`, `W/abc`, ""},
		{"weak_spaced", `W/ "abc"`, `W/abc`, ""},
		{"empty", "", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeETag(tc.raw); got != tc.wantNorm {
				t.Fatalf("normalizeETag(%q) = %q, want %q", tc.raw, got, tc.wantNorm)
			}
			if got := formatIfMatch(tc.raw); got != tc.ifM {
				t.Fatalf("formatIfMatch(%q) = %q, want %q", tc.raw, got, tc.ifM)
			}
		})
	}
}

// TestClientPutResourceWeakETagOmitsIfMatch verifies that a weak validator is
// not echoed into If-Match as a strong tag. Doing so makes the strong
// comparison fail server-side and produces perpetual 412s.
func TestClientPutResourceWeakETagOmitsIfMatch(t *testing.T) {
	t.Parallel()

	client, err := NewClient(putTestHTTPClient{do: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("If-Match"); got != "" {
			t.Fatalf("If-Match = %q, want empty for weak validator", got)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     http.Header{"Etag": []string{`W/"etag-after"`}},
			Body:       io.NopCloser(http.NoBody),
			Request:    req,
		}, nil
	}}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	etag, err := client.PutResource(context.Background(), "/cal/test.ics", testCalendar(t), `W/"etag-before"`)
	if err != nil {
		t.Fatalf("PutResource: %v", err)
	}
	if etag != `W/etag-after` {
		t.Fatalf("etag = %q, want %q", etag, `W/etag-after`)
	}
}

func TestClientDeleteResourceReportsGone(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		statusCode int
		wantGone   bool
		wantErr    bool
	}{
		{"not found", http.StatusNotFound, true, true},
		{"gone", http.StatusGone, true, true},
		{"no content", http.StatusNoContent, false, false},
		{"ok", http.StatusOK, false, false},
		{"server error", http.StatusInternalServerError, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewClient(putTestHTTPClient{do: func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodDelete {
					t.Fatalf("method = %s, want DELETE", req.Method)
				}
				if req.URL.String() != "https://example.com/cal/test.ics" {
					t.Fatalf("url = %s, want https://example.com/cal/test.ics", req.URL.String())
				}
				return &http.Response{
					StatusCode: tc.statusCode,
					Status:     http.StatusText(tc.statusCode),
					Body:       io.NopCloser(http.NoBody),
					Request:    req,
				}, nil
			}}, "https://example.com")
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			err = client.DeleteResource(context.Background(), "/cal/test.ics")
			if tc.wantErr != (err != nil) {
				t.Fatalf("DeleteResource err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got := errors.Is(err, ErrResourceGone); got != tc.wantGone {
				t.Fatalf("errors.Is(err, ErrResourceGone) = %v, want %v (err = %v)", got, tc.wantGone, err)
			}
		})
	}
}

func oversizedCalendarData(size int) string {
	var b strings.Builder
	b.Grow(size + 256)
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//chroncal//tests//EN\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("UID:oversized\r\n")
	b.WriteString("DTSTAMP:20260403T100000Z\r\n")
	b.WriteString("DTSTART:20260403T100000Z\r\n")
	b.WriteString("DTEND:20260403T110000Z\r\n")
	b.WriteString("SUMMARY:")
	b.WriteString(strings.Repeat("A", size))
	b.WriteString("\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

func TestClientGetResourceRejectsOversizedResponseBody(t *testing.T) {
	t.Parallel()

	client, err := NewClient(putTestHTTPClient{do: func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", req.Method)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/calendar; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(oversizedCalendarData(9 << 20))),
			Request:    req,
		}, nil
	}}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.GetResource(context.Background(), "/calendar/oversized.ics"); err == nil {
		t.Fatal("GetResource err = nil, want response size failure")
	}
}

func TestClientQueryAllRejectsOversizedResponseBody(t *testing.T) {
	t.Parallel()

	client, err := NewClient(putTestHTTPClient{do: func(req *http.Request) (*http.Response, error) {
		if req.Method != "REPORT" {
			t.Fatalf("method = %s, want REPORT", req.Method)
		}
		body := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/oversized.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-large&quot;</d:getetag>
        <cal:calendar-data>` + oversizedCalendarData(9<<20) + `</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.QueryAll(context.Background(), "/calendar/"); err == nil {
		t.Fatal("QueryAll err = nil, want response size failure")
	}
}
