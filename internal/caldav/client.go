package caldav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
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
const maxHTTPResponseBytes = 8 << 20

var defaultHTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
var errResponseTooLarge = errors.New("caldav response exceeds configured limits")

// Client wraps the go-webdav CalDAV client with error handling and auth.
type Client struct {
	httpClient webdav.HTTPClient
	inner      *caldav.Client
	endpoint   string
}

// NewClient creates a CalDAV client with the given HTTP client and endpoint.
// Use NewBasicAuthClient or NewBearerAuthClient for authenticated access.
func NewClient(httpClient webdav.HTTPClient, endpoint string) (*Client, error) {
	bounded := boundedHTTPClient{
		inner:            httpClient,
		maxResponseBytes: maxHTTPResponseBytes,
	}
	inner, err := caldav.NewClient(bounded, endpoint)
	if err != nil {
		return nil, fmt.Errorf("create caldav client: %w", err)
	}
	return &Client{httpClient: bounded, inner: inner, endpoint: endpoint}, nil
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
	calendarPath, err := c.CanonicalCollectionRef(calendarPath)
	if err != nil {
		return nil, err
	}

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
	calendarPath, err := c.CanonicalCollectionRef(calendarPath)
	if err != nil {
		return nil, err
	}

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

// CanonicalCollectionRef resolves a calendar collection href against the
// configured endpoint, validates it stays on the CalDAV origin, and returns a
// normalized server-relative path.
func (c *Client) CanonicalCollectionRef(ref string) (string, error) {
	endpointURL, err := url.Parse(c.endpoint)
	if err != nil {
		return "", fmt.Errorf("parse CalDAV endpoint: %w", err)
	}
	if !endpointURL.IsAbs() {
		return "", fmt.Errorf("CalDAV endpoint must be absolute")
	}

	resolved, err := resolveRef(endpointURL, ref)
	if err != nil {
		return "", fmt.Errorf("parse calendar href: %w", err)
	}
	if resolved.RawQuery != "" || resolved.Fragment != "" {
		return "", fmt.Errorf("calendar href must not include query or fragment")
	}
	if !sameOrigin(endpointURL, resolved) {
		return "", fmt.Errorf("calendar href must stay on the configured CalDAV origin")
	}

	collectionPath := normalizePath(resolved.Path)
	if collectionPath == "" {
		collectionPath = "/"
	}
	if !strings.HasSuffix(collectionPath, "/") {
		collectionPath += "/"
	}
	return collectionPath, nil
}

// CanonicalObjectRef resolves a calendar object href against the linked
// calendar collection, validates it stays on the configured CalDAV origin,
// and returns a normalized server-relative path.
//
// We intentionally do not require the object path to stay within the
// calendar collection's URL prefix: several CalDAV servers (GMX/Cosmo, for
// example) rewrite object hrefs at the server — a resource PUT at
// /cal/<user>/event.ics is reported back as /cal/<uuid>/event.ics. Enforcing
// a collection prefix would reject those same-origin hrefs and corrupt sync.
// Same-origin remains the security boundary.
func (c *Client) CanonicalObjectRef(calendarRef, objectRef string) (string, error) {
	collectionPath, err := c.CanonicalCollectionRef(calendarRef)
	if err != nil {
		return "", err
	}

	endpointURL, err := url.Parse(c.endpoint)
	if err != nil {
		return "", fmt.Errorf("parse CalDAV endpoint: %w", err)
	}
	baseURL, err := resolveRef(endpointURL, collectionPath)
	if err != nil {
		return "", fmt.Errorf("resolve calendar href: %w", err)
	}
	if !strings.HasSuffix(baseURL.Path, "/") {
		baseURL.Path += "/"
	}

	resolved, err := resolveRef(baseURL, objectRef)
	if err != nil {
		return "", fmt.Errorf("parse calendar object href: %w", err)
	}
	if resolved.RawQuery != "" || resolved.Fragment != "" {
		return "", fmt.Errorf("calendar object href must not include query or fragment")
	}
	if !sameOrigin(endpointURL, resolved) {
		return "", fmt.Errorf("calendar object href must stay on the configured CalDAV origin")
	}

	objectPath := normalizePath(resolved.Path)
	if objectPath == "" || strings.HasSuffix(objectPath, "/") {
		return "", fmt.Errorf("calendar object href must point to a resource, not a collection")
	}
	return objectPath, nil
}

func resolveRef(base *url.URL, ref string) (*url.URL, error) {
	rel, err := url.Parse(ref)
	if err != nil {
		return nil, err
	}
	return base.ResolveReference(rel), nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func normalizePath(raw string) string {
	if raw == "" {
		return ""
	}
	cleaned := path.Clean(raw)
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

type boundedHTTPClient struct {
	inner            webdav.HTTPClient
	maxResponseBytes int64
}

func (c boundedHTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.inner.Do(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &limitedReadCloser{
		inner:     resp.Body,
		remaining: c.maxResponseBytes,
	}
	return resp, nil
}

type limitedReadCloser struct {
	inner     io.ReadCloser
	remaining int64
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var extra [1]byte
		n, err := r.inner.Read(extra[:])
		switch {
		case n > 0 || err == nil:
			return 0, errResponseTooLarge
		case err == io.EOF:
			return 0, io.EOF
		default:
			return 0, err
		}
	}

	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.inner.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *limitedReadCloser) Close() error {
	return r.inner.Close()
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
