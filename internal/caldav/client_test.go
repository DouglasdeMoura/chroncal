package caldav

import (
	"context"
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
