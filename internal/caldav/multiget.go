package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"github.com/emersion/go-ical"
)

// MultiGetResult holds the outcome of a tolerant calendar-multiget REPORT.
// Resources that returned 200 with calendar-data land in Resources; paths the
// server reported as 404 (deleted between sync-collection and multiget) land
// in Missing so the caller can treat them as deletions instead of aborting
// the whole pull.
type MultiGetResult struct {
	Resources []Resource
	Missing   []string
}

// MultiGetTolerant fetches resources via the calendar-multiget REPORT and
// silently drops per-resource 404s instead of failing the whole batch.
//
// We can't use go-webdav's MultiGetCalendar for this because its
// decodeCalendarObjectList aborts the entire response when any single
// resource is missing the calendar-data property — which is exactly what a
// 404'd row looks like. Real servers (Google in particular) routinely hand
// us hrefs in sync-collection that are no longer there a few hundred
// milliseconds later by multiget time, so the intolerance was killing every
// pull on calendars with concurrent activity.
func (c *Client) MultiGetTolerant(ctx context.Context, calendarPath string, hrefs []string) (*MultiGetResult, error) {
	canonicalPath, err := c.CanonicalCollectionRef(calendarPath)
	if err != nil {
		return nil, err
	}
	if len(hrefs) == 0 {
		return &MultiGetResult{}, nil
	}

	body := buildMultiGetBody(hrefs)
	req, err := http.NewRequestWithContext(ctx, "REPORT", c.ResolveURL(canonicalPath), strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new REPORT request: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("REPORT calendar-multiget: HTTP %d", resp.StatusCode)
	}

	var ms multiGetMultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("decode multiget response: %w", err)
	}

	result := &MultiGetResult{}
	for _, r := range ms.Responses {
		href := strings.TrimSpace(r.Href)
		if href == "" {
			continue
		}
		// Top-level <status>404</status> means the entire resource is gone.
		if topCode := parseStatusCode(r.Status); topCode == http.StatusNotFound {
			result.Missing = append(result.Missing, href)
			continue
		}
		var etag, data string
		var ok bool
		for _, ps := range r.PropStats {
			code := parseStatusCode(ps.Status)
			if code == http.StatusNotFound {
				continue
			}
			if code >= 200 && code < 300 {
				etag = normalizeETag(ps.Prop.ETag)
				data = ps.Prop.CalendarData
				ok = true
				break
			}
		}
		if !ok || data == "" {
			result.Missing = append(result.Missing, href)
			continue
		}
		cal, parseErr := ical.NewDecoder(strings.NewReader(data)).Decode()
		if parseErr != nil {
			// Server returned a body we can't parse. Treat as missing rather
			// than aborting — the next sync will revisit the href.
			result.Missing = append(result.Missing, href)
			continue
		}
		result.Resources = append(result.Resources, Resource{
			Path: href,
			ETag: etag,
			Data: cal,
		})
	}
	return result, nil
}

func buildMultiGetBody(hrefs []string) string {
	var hrefXML strings.Builder
	for _, h := range hrefs {
		hrefXML.WriteString("  <d:href>")
		hrefXML.WriteString(xmlEscape(h))
		hrefXML.WriteString("</d:href>\n")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop>
    <d:getetag/>
    <c:calendar-data/>
  </d:prop>
%s</c:calendar-multiget>`, hrefXML.String())
}

type multiGetMultiStatus struct {
	XMLName   xml.Name           `xml:"DAV: multistatus"`
	Responses []multiGetResponse `xml:"DAV: response"`
}

type multiGetResponse struct {
	Href      string             `xml:"DAV: href"`
	Status    string             `xml:"DAV: status"`
	PropStats []multiGetPropStat `xml:"DAV: propstat"`
}

type multiGetPropStat struct {
	Status string `xml:"DAV: status"`
	Prop   struct {
		ETag         string `xml:"DAV: getetag"`
		CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	} `xml:"DAV: prop"`
}
