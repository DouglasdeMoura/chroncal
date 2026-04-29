package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-webdav"
)

type verifyMultiStatus struct {
	Responses []verifyResponse `xml:"DAV: response"`
}

type verifyResponse struct {
	PropStats []verifyPropStat `xml:"DAV: propstat"`
}

type verifyPropStat struct {
	Status string        `xml:"DAV: status"`
	Prop   verifyPropSet `xml:"DAV: prop"`
}

type verifyPropSet struct {
	DisplayName   string               `xml:"DAV: displayname"`
	ResourceType  verifyResourceTypeEl `xml:"DAV: resourcetype"`
	CalendarColor string               `xml:"http://apple.com/ns/ical/ calendar-color"`
}

type verifyResourceTypeEl struct {
	Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
}

// CalendarMetadata holds the user-visible properties advertised by a CalDAV
// calendar collection: the server's display name and the Apple-style
// calendar-color extension (used by Google, Apple, Fastmail, and others).
type CalendarMetadata struct {
	DisplayName string
	Color       string
}

// VerifyCalendarURL performs a PROPFIND at the user-supplied calendar URL to
// confirm that authentication succeeds and the resource is a CalDAV calendar
// collection. Unlike principal discovery, it tests the exact URL the caller
// provided — the right behaviour for a "Test connection" button where the
// user has already entered a calendar URL.
//
// Returns the calendar's displayname and calendar-color when advertised by
// the server.
func VerifyCalendarURL(ctx context.Context, calendarURL, username, password, authType string, allowInsecure bool) (CalendarMetadata, error) {
	parsed, httpClient, err := buildVerifyClient(calendarURL, username, password, authType, allowInsecure)
	if err != nil {
		return CalendarMetadata{}, err
	}
	return fetchCalendarMetadata(ctx, parsed.String(), httpClient, true)
}

// FetchCalendarMetadata performs the same PROPFIND as VerifyCalendarURL but
// using a credential — handy for picking up the remote display name and
// calendar-color at calendar-link time, before the first sync runs.
func FetchCalendarMetadata(ctx context.Context, calendarURL, username, password, authType string, allowInsecure bool) (CalendarMetadata, error) {
	parsed, httpClient, err := buildVerifyClient(calendarURL, username, password, authType, allowInsecure)
	if err != nil {
		return CalendarMetadata{}, err
	}
	return fetchCalendarMetadata(ctx, parsed.String(), httpClient, false)
}

func buildVerifyClient(calendarURL, username, password, authType string, allowInsecure bool) (*url.URL, webdav.HTTPClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(calendarURL))
	if err != nil {
		return nil, nil, fmt.Errorf("parse calendar URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, nil, fmt.Errorf("calendar URL must include scheme and host")
	}
	if parsed.Scheme != "https" && !allowInsecure {
		return nil, nil, fmt.Errorf("calendar URL must use HTTPS; allow-insecure is required for HTTP (e.g., local development)")
	}

	var httpClient webdav.HTTPClient
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "bearer", "oauth2":
		httpClient = &bearerHTTPClient{inner: defaultHTTPClient, token: password}
	default:
		httpClient = webdav.HTTPClientWithBasicAuth(defaultHTTPClient, username, password)
	}
	httpClient = boundedHTTPClient{inner: httpClient, maxResponseBytes: maxHTTPResponseBytes}
	return parsed, httpClient, nil
}

func fetchCalendarMetadata(ctx context.Context, calendarURL string, httpClient webdav.HTTPClient, requireCalendar bool) (CalendarMetadata, error) {
	const body = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ic="http://apple.com/ns/ical/">
  <d:prop>
    <d:resourcetype/>
    <d:displayname/>
    <ic:calendar-color/>
  </d:prop>
</d:propfind>`

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", calendarURL, strings.NewReader(body))
	if err != nil {
		return CalendarMetadata{}, fmt.Errorf("new PROPFIND request: %w", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return CalendarMetadata{}, fmt.Errorf("verify calendar: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return CalendarMetadata{}, fmt.Errorf("authentication failed — check username and password (HTTP 401)")
	case http.StatusForbidden:
		return CalendarMetadata{}, fmt.Errorf("access denied — credentials accepted but this URL is not reachable (HTTP 403)")
	case http.StatusNotFound:
		return CalendarMetadata{}, fmt.Errorf("calendar not found at this URL (HTTP 404)")
	case http.StatusMultiStatus:
	default:
		return CalendarMetadata{}, httpError(resp)
	}

	var ms verifyMultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return CalendarMetadata{}, fmt.Errorf("decode PROPFIND response: %w", err)
	}

	meta := CalendarMetadata{}
	isCalendar := false
	for _, r := range ms.Responses {
		for _, ps := range r.PropStats {
			code := parseStatusCode(ps.Status)
			if code < 200 || code >= 300 {
				continue
			}
			if meta.DisplayName == "" {
				meta.DisplayName = strings.TrimSpace(ps.Prop.DisplayName)
			}
			if meta.Color == "" {
				meta.Color = strings.TrimSpace(ps.Prop.CalendarColor)
			}
			if ps.Prop.ResourceType.Calendar != nil {
				isCalendar = true
			}
		}
	}
	if requireCalendar && !isCalendar {
		return meta, fmt.Errorf("URL is reachable but does not point to a CalDAV calendar collection")
	}
	return meta, nil
}
