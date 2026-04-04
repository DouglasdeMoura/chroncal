package caldav

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// RemoteCalendar holds info about a calendar discovered on a CalDAV server.
type RemoteCalendar struct {
	Path                  string
	Name                  string
	Description           string
	SupportedComponentSet []string // e.g. ["VEVENT", "VTODO", "VJOURNAL"]
}

// Resource represents a single iCal resource fetched from the server.
type Resource struct {
	Path string
	ETag string
	Data *ical.Calendar
}

// Change represents a changed or deleted resource from a sync-collection report.
type Change struct {
	Path    string
	ETag    string
	Deleted bool
}

const defaultHTTPTimeout = 30 * time.Second

var defaultHTTPClient = &http.Client{Timeout: defaultHTTPTimeout}

// Client wraps the go-webdav CalDAV client with error handling and auth.
type Client struct {
	httpClient webdav.HTTPClient
	inner      *caldav.Client
	endpoint   string
}

// NewClient creates a CalDAV client with the given HTTP client and endpoint.
// Use NewBasicAuthClient or NewBearerAuthClient for authenticated access.
func NewClient(httpClient webdav.HTTPClient, endpoint string) (*Client, error) {
	inner, err := caldav.NewClient(httpClient, endpoint)
	if err != nil {
		return nil, fmt.Errorf("create caldav client: %w", err)
	}
	return &Client{httpClient: httpClient, inner: inner, endpoint: endpoint}, nil
}

// NewBasicAuthClient creates a CalDAV client with HTTP basic authentication.
func NewBasicAuthClient(endpoint, username, password string) (*Client, error) {
	httpClient := webdav.HTTPClientWithBasicAuth(defaultHTTPClient, username, password)
	return NewClient(httpClient, endpoint)
}

// NewBearerAuthClient creates a CalDAV client with Bearer token authentication.
func NewBearerAuthClient(endpoint, token string) (*Client, error) {
	httpClient := &bearerHTTPClient{inner: defaultHTTPClient, token: token}
	return NewClient(httpClient, endpoint)
}

// DiscoverCalendars finds the user's calendars on the server.
func (c *Client) DiscoverCalendars(ctx context.Context) ([]RemoteCalendar, error) {
	principal, err := c.inner.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("find principal: %w", err)
	}

	homeSet, err := c.inner.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("find calendar home set: %w", err)
	}

	found, err := c.inner.FindCalendars(ctx, homeSet)
	if err != nil {
		return nil, fmt.Errorf("find calendars: %w", err)
	}

	out := make([]RemoteCalendar, len(found))
	for i, cal := range found {
		out[i] = RemoteCalendar{
			Path:                  cal.Path,
			Name:                  cal.Name,
			Description:           cal.Description,
			SupportedComponentSet: cal.SupportedComponentSet,
		}
	}
	return out, nil
}

// GetResources fetches full iCal data for a set of hrefs via calendar-multiget.
func (c *Client) GetResources(ctx context.Context, calendarPath string, hrefs []string) ([]Resource, error) {
	multiGet := &caldav.CalendarMultiGet{
		Paths: hrefs,
		CompRequest: caldav.CalendarCompRequest{
			Name:     "VCALENDAR",
			AllComps: true,
			AllProps: true,
		},
	}

	objects, err := c.inner.MultiGetCalendar(ctx, calendarPath, multiGet)
	if err != nil {
		return nil, fmt.Errorf("multiget: %w", err)
	}

	out := make([]Resource, 0, len(objects))
	for _, obj := range objects {
		out = append(out, Resource{
			Path: obj.Path,
			ETag: normalizeETag(obj.ETag),
			Data: obj.Data,
		})
	}
	return out, nil
}

// QueryAll fetches all resources from a calendar.
func (c *Client) QueryAll(ctx context.Context, calendarPath string) ([]Resource, error) {
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     "VCALENDAR",
			AllComps: true,
			AllProps: true,
		},
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
		},
	}

	objects, err := c.inner.QueryCalendar(ctx, calendarPath, query)
	if err != nil {
		return nil, fmt.Errorf("query all: %w", err)
	}

	out := make([]Resource, 0, len(objects))
	for _, obj := range objects {
		out = append(out, Resource{
			Path: obj.Path,
			ETag: normalizeETag(obj.ETag),
			Data: obj.Data,
		})
	}
	return out, nil
}

// PutResource uploads a single iCal resource. Returns the new ETag.
// If etag is non-empty, the server will reject the PUT if the resource was
// modified since (If-Match precondition).
func (c *Client) PutResource(ctx context.Context, path string, data *ical.Calendar, etag string) (string, error) {
	body, err := EncodeCalendar(data)
	if err != nil {
		return "", fmt.Errorf("encode calendar: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.ResolveURL(path), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new PUT request: %w", err)
	}
	req.Header.Set("Content-Type", ical.MIMEType)
	if etag != "" {
		req.Header.Set("If-Match", formatIfMatch(etag))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("put resource: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("put resource: %w", httpError(resp))
	}

	return normalizeETag(resp.Header.Get("ETag")), nil
}

// DeleteResource removes a resource by path.
func (c *Client) DeleteResource(ctx context.Context, path string) error {
	return c.inner.RemoveAll(ctx, path)
}

// GetResource fetches a single calendar object by path.
func (c *Client) GetResource(ctx context.Context, path string) (*Resource, error) {
	obj, err := c.inner.GetCalendarObject(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get resource: %w", err)
	}
	return &Resource{
		Path: obj.Path,
		ETag: normalizeETag(obj.ETag),
		Data: obj.Data,
	}, nil
}

// HTTPClient exposes the authenticated HTTP client for raw WebDAV requests.
func (c *Client) HTTPClient() webdav.HTTPClient {
	return c.httpClient
}

// ResolveURL resolves a discovered calendar href against the client's endpoint.
// CalDAV discovery commonly returns server-relative paths.
func (c *Client) ResolveURL(ref string) string {
	if ref == "" {
		return ref
	}
	if parsed, err := url.Parse(ref); err == nil && parsed.IsAbs() {
		return ref
	}

	base, err := url.Parse(c.endpoint)
	if err != nil {
		return ref
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(rel).String()
}

// EncodeCalendar serializes an ical.Calendar to bytes.
func EncodeCalendar(cal *ical.Calendar) ([]byte, error) {
	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func httpError(resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("HTTP 0")
	}

	status := strings.TrimSpace(resp.Status)
	if status == "" {
		status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var bodyText string
	if resp.Body != nil {
		lr := &io.LimitedReader{R: resp.Body, N: 1024}
		body, _ := io.ReadAll(lr)
		bodyText = strings.TrimSpace(string(body))
		if lr.N == 0 {
			bodyText += " […]"
		}
	}

	if bodyText == "" {
		return fmt.Errorf("HTTP %s", status)
	}
	return fmt.Errorf("HTTP %s: %s", status, bodyText)
}

func normalizeETag(etag string) string {
	etag = strings.TrimSpace(etag)
	etag = strings.TrimPrefix(etag, "W/")
	etag = strings.TrimSpace(etag)
	return strings.Trim(etag, `"`)
}

func formatIfMatch(etag string) string {
	etag = normalizeETag(etag)
	if etag == "" {
		return ""
	}
	return `"` + etag + `"`
}

// bearerHTTPClient adds a Bearer token to every request.
type bearerHTTPClient struct {
	inner *http.Client
	token string
}

func (c *bearerHTTPClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.inner.Do(req)
}
