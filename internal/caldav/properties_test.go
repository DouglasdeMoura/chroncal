package caldav

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (c testHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.do(req)
}

func TestGetCalendarColor(t *testing.T) {
	t.Parallel()

	client := testHTTPClient{
		do: func(req *http.Request) (*http.Response, error) {
			if req.Method != "PROPFIND" {
				t.Fatalf("method = %s, want PROPFIND", req.Method)
			}
			if req.URL.String() != "https://example.com/cal/work" {
				t.Fatalf("url = %s", req.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>/cal/work</d:href>
    <d:propstat>
      <d:prop>
        <ic:calendar-color>#aabbcc</ic:calendar-color>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
			}, nil
		},
	}

	color, err := GetCalendarColor(context.Background(), client, "https://example.com/cal/work")
	if err != nil {
		t.Fatalf("GetCalendarColor: %v", err)
	}
	if color != "#aabbcc" {
		t.Fatalf("color = %q, want #aabbcc", color)
	}
}

func TestNormalizeCalendarColor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want string
	}{
		{"#9FE1E7FF", "#9FE1E7"},
		{"  #9FE1E7FF ", "#9FE1E7"},
		{"9FE1E7FF", "#9FE1E7"},
		{"#9FE1E7", "#9FE1E7"},
		{"9FE1E7", "#9FE1E7"},
		{"", ""},
		{"red", "red"},
	}
	for _, c := range cases {
		if got := NormalizeCalendarColor(c.in); got != c.want {
			t.Errorf("NormalizeCalendarColor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSetCalendarColor(t *testing.T) {
	t.Parallel()

	client := testHTTPClient{
		do: func(req *http.Request) (*http.Response, error) {
			if req.Method != "PROPPATCH" {
				t.Fatalf("method = %s, want PROPPATCH", req.Method)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			text := string(body)
			if !strings.Contains(text, "<ic:calendar-color>#112233</ic:calendar-color>") {
				t.Fatalf("body missing calendar color: %s", text)
			}
			return &http.Response{
				StatusCode: http.StatusMultiStatus,
				Header:     http.Header{"Content-Type": []string{"application/xml"}},
				Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/cal/work</d:href>
    <d:propstat>
      <d:prop />
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
			}, nil
		},
	}

	if err := SetCalendarColor(context.Background(), client, "https://example.com/cal/work", "#112233"); err != nil {
		t.Fatalf("SetCalendarColor: %v", err)
	}
}

func TestClientGetCalendarColor_ResolvesRelativeHref(t *testing.T) {
	t.Parallel()

	client := &Client{
		httpClient: testHTTPClient{
			do: func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "http://app/remote.php/dav/calendars/admin/personal/" {
					t.Fatalf("url = %s", req.URL.String())
				}
				return &http.Response{
					StatusCode: http.StatusMultiStatus,
					Header:     http.Header{"Content-Type": []string{"application/xml"}},
					Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:propstat>
      <d:prop>
        <ic:calendar-color>#aabbcc</ic:calendar-color>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
				}, nil
			},
		},
		endpoint: "http://app/remote.php/dav",
	}

	color, err := client.GetCalendarColor(context.Background(), "/remote.php/dav/calendars/admin/personal/")
	if err != nil {
		t.Fatalf("GetCalendarColor: %v", err)
	}
	if color != "#aabbcc" {
		t.Fatalf("color = %q, want #aabbcc", color)
	}
}

func TestClientSetCalendarColor_ResolvesRelativeHref(t *testing.T) {
	t.Parallel()

	client := &Client{
		httpClient: testHTTPClient{
			do: func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "http://app/remote.php/dav/calendars/admin/personal/" {
					t.Fatalf("url = %s", req.URL.String())
				}
				return &http.Response{
					StatusCode: http.StatusMultiStatus,
					Header:     http.Header{"Content-Type": []string{"application/xml"}},
					Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:propstat>
      <d:prop />
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`)),
				}, nil
			},
		},
		endpoint: "http://app/remote.php/dav",
	}

	if err := client.SetCalendarColor(context.Background(), "/remote.php/dav/calendars/admin/personal/", "#112233"); err != nil {
		t.Fatalf("SetCalendarColor: %v", err)
	}
}
