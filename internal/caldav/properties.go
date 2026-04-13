package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/emersion/go-webdav"
)

type calendarColorMultiStatus struct {
	Responses []calendarColorResponse `xml:"DAV: response"`
}

type calendarColorResponse struct {
	PropStats []calendarColorPropStat `xml:"DAV: propstat"`
}

type calendarColorPropStat struct {
	Status string `xml:"DAV: status"`
	Prop   struct {
		CalendarColor string `xml:"http://apple.com/ns/ical/ calendar-color"`
	} `xml:"DAV: prop"`
}

// GetCalendarColor fetches the current calendar-color property for a calendar.
func GetCalendarColor(ctx context.Context, httpClient webdav.HTTPClient, calendarURL string) (string, error) {
	const body = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:prop>
    <ic:calendar-color />
  </d:prop>
</d:propfind>`

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", calendarURL, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new PROPFIND request: %w", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		return "", fmt.Errorf("PROPFIND calendar-color: HTTP %d", resp.StatusCode)
	}

	var ms calendarColorMultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return "", fmt.Errorf("decode PROPFIND response: %w", err)
	}

	for _, response := range ms.Responses {
		for _, propstat := range response.PropStats {
			code := parseStatusCode(propstat.Status)
			switch {
			case code >= 200 && code < 300:
				return strings.TrimSpace(propstat.Prop.CalendarColor), nil
			case code == http.StatusNotFound:
				return "", nil
			case code != 0:
				return "", fmt.Errorf("PROPFIND calendar-color: HTTP %d", code)
			}
		}
	}

	return "", nil
}

// GetCalendarColor fetches the current calendar-color for a calendar href.
func (c *Client) GetCalendarColor(ctx context.Context, calendarURL string) (string, error) {
	canonicalURL, err := c.CanonicalCollectionRef(calendarURL)
	if err != nil {
		return "", err
	}
	return GetCalendarColor(ctx, c.httpClient, c.ResolveURL(canonicalURL))
}

// SetCalendarColor updates the calendar-color property for a calendar.
func SetCalendarColor(ctx context.Context, httpClient webdav.HTTPClient, calendarURL, color string) error {
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:propertyupdate xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:set>
    <d:prop>
      <ic:calendar-color>%s</ic:calendar-color>
    </d:prop>
  </d:set>
</d:propertyupdate>`, xmlEscape(color))

	req, err := http.NewRequestWithContext(ctx, "PROPPATCH", calendarURL, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("new PROPPATCH request: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusMultiStatus:
		var ms calendarColorMultiStatus
		if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
			return fmt.Errorf("decode PROPPATCH response: %w", err)
		}
		for _, response := range ms.Responses {
			for _, propstat := range response.PropStats {
				code := parseStatusCode(propstat.Status)
				if code < 200 || code >= 300 {
					return fmt.Errorf("PROPPATCH calendar-color: HTTP %d", code)
				}
			}
		}
		return nil
	default:
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("PROPPATCH calendar-color: HTTP %d", resp.StatusCode)
	}
}

// SetCalendarColor updates the calendar-color property for a calendar href.
func (c *Client) SetCalendarColor(ctx context.Context, calendarURL, color string) error {
	canonicalURL, err := c.CanonicalCollectionRef(calendarURL)
	if err != nil {
		return err
	}
	return SetCalendarColor(ctx, c.httpClient, c.ResolveURL(canonicalURL), color)
}

func parseStatusCode(status string) int {
	fields := strings.Fields(status)
	if len(fields) < 2 {
		return 0
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return code
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}
