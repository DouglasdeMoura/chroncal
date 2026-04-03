package caldav

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestQueryFreeBusy(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	client := testHTTPClient{
		do: func(req *http.Request) (*http.Response, error) {
			if req.Method != "REPORT" {
				t.Fatalf("method = %s, want REPORT", req.Method)
			}
			if req.URL.String() != "https://example.com/cal/work" {
				t.Fatalf("url = %s", req.URL.String())
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			text := string(body)
			if !strings.Contains(text, `start="20260410T000000Z"`) {
				t.Fatalf("body missing start time: %s", text)
			}
			if !strings.Contains(text, `end="20260411T000000Z"`) {
				t.Fatalf("body missing end time: %s", text)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/calendar; charset=utf-8"}},
				Body: io.NopCloser(strings.NewReader(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VFREEBUSY
UID:fb-1
DTSTAMP:20260403T120000Z
DTSTART:20260410T000000Z
DTEND:20260411T000000Z
FREEBUSY:20260410T090000Z/20260410T100000Z
END:VFREEBUSY
END:VCALENDAR`)),
			}, nil
		},
	}

	result, err := QueryFreeBusy(context.Background(), client, "https://example.com/cal/work", from, to)
	if err != nil {
		t.Fatalf("QueryFreeBusy: %v", err)
	}
	if result.UID != "fb-1" {
		t.Fatalf("UID = %q, want fb-1", result.UID)
	}
	if len(result.Periods) != 1 {
		t.Fatalf("periods = %d, want 1", len(result.Periods))
	}
}

func TestClientQueryFreeBusy_ResolvesRelativeHref(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	client := &Client{
		httpClient: testHTTPClient{
			do: func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "http://app/remote.php/dav/calendars/admin/personal/" {
					t.Fatalf("url = %s", req.URL.String())
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/calendar; charset=utf-8"}},
					Body: io.NopCloser(strings.NewReader(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VFREEBUSY
UID:fb-1
DTSTAMP:20260403T120000Z
DTSTART:20260410T000000Z
DTEND:20260411T000000Z
FREEBUSY:20260410T090000Z/20260410T100000Z
END:VFREEBUSY
END:VCALENDAR`)),
				}, nil
			},
		},
		endpoint: "http://app/remote.php/dav",
	}

	result, err := client.QueryFreeBusy(context.Background(), "/remote.php/dav/calendars/admin/personal/", from, to)
	if err != nil {
		t.Fatalf("QueryFreeBusy: %v", err)
	}
	if result.UID != "fb-1" {
		t.Fatalf("UID = %q, want fb-1", result.UID)
	}
}
