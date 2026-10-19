package caldav

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// MultiGetTolerant fetches resources via the calendar-multiget REPORT. It
// reports a per-resource 404 or a body that does not parse in the result
// instead of a fail of the whole batch. See decodeTolerantReport.
func (c *Client) MultiGetTolerant(ctx context.Context, calendarPath string, hrefs []string) (*ReportResult, error) {
	canonicalPath, err := c.CanonicalCollectionRef(calendarPath)
	if err != nil {
		return nil, err
	}
	if len(hrefs) == 0 {
		return &ReportResult{}, nil
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
		return nil, fmt.Errorf("REPORT calendar-multiget: %w", httpError(resp))
	}

	return decodeTolerantReport(resp.Body)
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
