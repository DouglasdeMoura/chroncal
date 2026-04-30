package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func newSyncCollectionClient(t *testing.T, do func(*http.Request) (*http.Response, error)) *Client {
	t.Helper()
	client, err := NewClient(putTestHTTPClient{do: do}, "https://example.com")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestSyncCollectionParsesUpdatesAndDeletions(t *testing.T) {
	t.Parallel()

	const responseBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/calendar/changed.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-new&quot;</d:getetag>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/calendar/removed.ics</d:href>
    <d:status>HTTP/1.1 404 Not Found</d:status>
  </d:response>
  <d:sync-token>https://example.com/sync/abc-123</d:sync-token>
</d:multistatus>`

	var capturedBody string
	client := newSyncCollectionClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "REPORT" {
			t.Fatalf("method = %s, want REPORT", req.Method)
		}
		if got := req.Header.Get("Depth"); got != "1" {
			t.Fatalf("Depth = %q, want 1", got)
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		capturedBody = string(raw)
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    req,
		}, nil
	})

	result, err := client.SyncCollection(context.Background(), "/calendar/", "prev-token")
	if err != nil {
		t.Fatalf("SyncCollection: %v", err)
	}
	if !strings.Contains(capturedBody, "<d:sync-token>prev-token</d:sync-token>") {
		t.Fatalf("request body missing token:\n%s", capturedBody)
	}
	if result.SyncToken != "https://example.com/sync/abc-123" {
		t.Fatalf("SyncToken = %q, want %q", result.SyncToken, "https://example.com/sync/abc-123")
	}
	if len(result.Changes) != 2 {
		t.Fatalf("Changes = %d, want 2", len(result.Changes))
	}

	var added, removed *SyncChange
	for i := range result.Changes {
		switch result.Changes[i].Path {
		case "/calendar/changed.ics":
			added = &result.Changes[i]
		case "/calendar/removed.ics":
			removed = &result.Changes[i]
		}
	}
	if added == nil || added.Deleted || added.ETag != "etag-new" {
		t.Fatalf("added entry wrong: %+v", added)
	}
	if removed == nil || !removed.Deleted {
		t.Fatalf("removed entry wrong: %+v", removed)
	}
}

func TestSyncCollectionDetectsInvalidToken(t *testing.T) {
	t.Parallel()

	const responseBody = `<?xml version="1.0" encoding="utf-8"?>
<d:error xmlns:d="DAV:">
  <d:valid-sync-token/>
</d:error>`

	client := newSyncCollectionClient(t, func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    req,
		}, nil
	})

	_, err := client.SyncCollection(context.Background(), "/calendar/", "stale-token")
	if !errors.Is(err, ErrSyncTokenInvalid) {
		t.Fatalf("err = %v, want ErrSyncTokenInvalid", err)
	}
}

func TestSyncCollectionForbiddenWithoutPreconditionIsGenericError(t *testing.T) {
	t.Parallel()

	client := newSyncCollectionClient(t, func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("nope")),
			Request:    req,
		}, nil
	})

	_, err := client.SyncCollection(context.Background(), "/calendar/", "")
	if err == nil {
		t.Fatal("err = nil, want generic 403 error")
	}
	if errors.Is(err, ErrSyncTokenInvalid) {
		t.Fatalf("err = %v, must not be ErrSyncTokenInvalid without precondition body", err)
	}
	if errors.Is(err, ErrSyncCollectionUnsupported) {
		t.Fatalf("err = %v, must not be ErrSyncCollectionUnsupported on 403", err)
	}
}

func TestSyncCollectionUnsupportedOnNotImplemented(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status int
	}{
		{"501 Not Implemented", http.StatusNotImplemented},
		{"405 Method Not Allowed", http.StatusMethodNotAllowed},
		{"400 Bad Request", http.StatusBadRequest},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := newSyncCollectionClient(t, func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Status:     tc.name,
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    req,
				}, nil
			})
			_, err := client.SyncCollection(context.Background(), "/calendar/", "")
			if !errors.Is(err, ErrSyncCollectionUnsupported) {
				t.Fatalf("err = %v, want ErrSyncCollectionUnsupported", err)
			}
		})
	}
}

func TestSyncCollectionEscapesTokenInRequestBody(t *testing.T) {
	t.Parallel()

	var capturedBody string
	client := newSyncCollectionClient(t, func(req *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		capturedBody = string(raw)
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:"><d:sync-token>new</d:sync-token></d:multistatus>`)),
			Request: req,
		}, nil
	})

	if _, err := client.SyncCollection(context.Background(), "/calendar/", `evil<token>&"value"`); err != nil {
		t.Fatalf("SyncCollection: %v", err)
	}
	if strings.Contains(capturedBody, "<token>") {
		t.Fatalf("token unescaped in body:\n%s", capturedBody)
	}
	if !strings.Contains(capturedBody, "&lt;token&gt;") {
		t.Fatalf("token escape missing in body:\n%s", capturedBody)
	}
}
