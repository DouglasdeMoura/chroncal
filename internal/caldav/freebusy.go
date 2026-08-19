package caldav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
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
		_, _ = io.Copy(io.Discard, resp.Body)
		return freebusy.Result{}, statusErrorf(resp.StatusCode, "free-busy query: HTTP %d", resp.StatusCode)
	}

	results, err := freebusy.ParseCalendar(resp.Body)
	if err != nil {
		return freebusy.Result{}, fmt.Errorf("parse REPORT response: %w", err)
	}
	return mergeFreeBusyResults(results, from, to), nil
}

func mergeFreeBusyResults(results []freebusy.Result, from, to time.Time) freebusy.Result {
	from = from.UTC()
	to = to.UTC()
	if len(results) == 0 {
		return freebusy.Result{Start: from, End: to}
	}

	merged := freebusy.Result{
		UID:       results[0].UID,
		Organizer: results[0].Organizer,
		URL:       results[0].URL,
		DTStamp:   results[0].DTStamp,
	}
	var start, end time.Time
	for _, next := range results {
		if !next.Start.IsZero() && (start.IsZero() || next.Start.Before(start)) {
			start = next.Start
		}
		if !next.End.IsZero() && (end.IsZero() || next.End.After(end)) {
			end = next.End
		}
		if merged.UID == "" {
			merged.UID = next.UID
		}
		if merged.Organizer == "" {
			merged.Organizer = next.Organizer
		}
		if merged.URL == "" {
			merged.URL = next.URL
		}
		if !next.DTStamp.IsZero() && (merged.DTStamp.IsZero() || next.DTStamp.After(merged.DTStamp)) {
			merged.DTStamp = next.DTStamp
		}
		merged.Periods = append(merged.Periods, next.Periods...)
	}
	if start.IsZero() {
		start = from
	}
	if end.IsZero() {
		end = to
	}
	merged.Start = start
	merged.End = end
	// Radicale uses DTSTART/DTEND as the busy interval. Use the requested range.
	if windowMatchesPeriodSpan(merged) {
		merged.Start = from
		merged.End = to
	}
	sort.Slice(merged.Periods, func(i, j int) bool {
		if merged.Periods[i].Start.Equal(merged.Periods[j].Start) {
			return merged.Periods[i].End.Before(merged.Periods[j].End)
		}
		return merged.Periods[i].Start.Before(merged.Periods[j].Start)
	})
	return merged
}

func windowMatchesPeriodSpan(result freebusy.Result) bool {
	if len(result.Periods) == 0 {
		return false
	}
	spanStart := result.Periods[0].Start
	spanEnd := result.Periods[0].End
	for _, period := range result.Periods[1:] {
		if period.Start.Before(spanStart) {
			spanStart = period.Start
		}
		if period.End.After(spanEnd) {
			spanEnd = period.End
		}
	}
	return result.Start.Equal(spanStart) && result.End.Equal(spanEnd)
}

// QueryFreeBusy executes a free-busy-query REPORT using the client's authenticated HTTP transport.
func (c *Client) QueryFreeBusy(ctx context.Context, calendarURL string, from, to time.Time) (freebusy.Result, error) {
	canonicalURL, err := c.CanonicalCollectionRef(calendarURL)
	if err != nil {
		return freebusy.Result{}, err
	}
	return QueryFreeBusy(ctx, c.httpClient, c.ResolveURL(canonicalURL), from, to)
}
