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
	DisplayName  string               `xml:"DAV: displayname"`
	ResourceType verifyResourceTypeEl `xml:"DAV: resourcetype"`
}

type verifyResourceTypeEl struct {
	Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
}

// VerifyCalendarURL performs a PROPFIND at the user-supplied calendar URL to
// confirm that authentication succeeds and the resource is a CalDAV calendar
// collection. Unlike principal discovery, it tests the exact URL the caller
// provided — the right behaviour for a "Test connection" button where the
// user has already entered a calendar URL.
//
// Returns the calendar's displayname when advertised by the server.
func VerifyCalendarURL(ctx context.Context, calendarURL, username, password, authType string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(calendarURL))
	if err != nil {
		return "", fmt.Errorf("parse calendar URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("calendar URL must include scheme and host")
	}
	if parsed.Scheme != "https" && !allowInsecure {
		return "", fmt.Errorf("calendar URL must use HTTPS; allow-insecure is required for HTTP (e.g., local development)")
	}

	var httpClient webdav.HTTPClient
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "bearer":
		httpClient = &bearerHTTPClient{inner: defaultHTTPClient, token: password}
	default:
		httpClient = webdav.HTTPClientWithBasicAuth(defaultHTTPClient, username, password)
	}
	httpClient = boundedHTTPClient{inner: httpClient, maxResponseBytes: maxHTTPResponseBytes}

	const body = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop>
    <d:resourcetype/>
    <d:displayname/>
  </d:prop>
</d:propfind>`

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", parsed.String(), strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new PROPFIND request: %w", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("verify calendar: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return "", fmt.Errorf("authentication failed — check username and password (HTTP 401)")
	case http.StatusForbidden:
		return "", fmt.Errorf("access denied — credentials accepted but this URL is not reachable (HTTP 403)")
	case http.StatusNotFound:
		return "", fmt.Errorf("calendar not found at this URL (HTTP 404)")
	case http.StatusMultiStatus:
	default:
		return "", httpError(resp)
	}

	var ms verifyMultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return "", fmt.Errorf("decode PROPFIND response: %w", err)
	}

	var displayName string
	isCalendar := false
	for _, r := range ms.Responses {
		for _, ps := range r.PropStats {
			code := parseStatusCode(ps.Status)
			if code < 200 || code >= 300 {
				continue
			}
			if displayName == "" {
				displayName = strings.TrimSpace(ps.Prop.DisplayName)
			}
			if ps.Prop.ResourceType.Calendar != nil {
				isCalendar = true
			}
		}
	}
	if !isCalendar {
		return displayName, fmt.Errorf("URL is reachable but does not point to a CalDAV calendar collection")
	}
	return displayName, nil
}
