package caldav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-webdav"

	"github.com/douglasdemoura/chroncal/internal/freebusy"
)

// QueryFreeBusy executes a raw CalDAV free-busy-query REPORT against a calendar.
func QueryFreeBusy(ctx context.Context, httpClient webdav.HTTPClient, calendarURL string, from, to time.Time) (freebusy.Result, error) {
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<c:free-busy-query xmlns:c="urn:ietf:params:xml:ns:caldav">
  <c:time-range start="%s" end="%s"/>
</c:free-busy-query>`,
		from.UTC().Format("20060102T150405Z"),
		to.UTC().Format("20060102T150405Z"),
	)

	req, err := http.NewRequestWithContext(ctx, "REPORT", calendarURL, strings.NewReader(body))
	if err != nil {
		return freebusy.Result{}, fmt.Errorf("new REPORT request: %w", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Accept", "text/calendar")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return freebusy.Result{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		io.Copy(io.Discard, resp.Body)
		return freebusy.Result{}, fmt.Errorf("free-busy query: HTTP %d", resp.StatusCode)
	}

	results, err := freebusy.ParseCalendar(resp.Body)
	if err != nil {
		return freebusy.Result{}, fmt.Errorf("parse REPORT response: %w", err)
	}
	if len(results) == 0 {
		return freebusy.Result{Start: from.UTC(), End: to.UTC()}, nil
	}

	result := results[0]
	if result.Start.IsZero() {
		result.Start = from.UTC()
	}
	if result.End.IsZero() {
		result.End = to.UTC()
	}
	return result, nil
}

// QueryFreeBusy executes a free-busy-query REPORT using the client's authenticated HTTP transport.
func (c *Client) QueryFreeBusy(ctx context.Context, calendarURL string, from, to time.Time) (freebusy.Result, error) {
	return QueryFreeBusy(ctx, c.httpClient, calendarURL, from, to)
}
