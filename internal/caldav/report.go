package caldav

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/emersion/go-ical"
)

// ReportResult holds the outcome of a tolerant REPORT: a calendar-multiget
// (MultiGetTolerant) or a calendar-query (QueryAll). A resource that
// returned 200 with a calendar-data body that parses lands in Resources.
// A path that the server reported as 404, or without calendar-data, lands
// in Missing. A path whose 200 body the ical decoder rejects lands in
// Unparseable. One bad href then does not abort the whole report.
//
// A Missing entry is not evidence of a deletion: a server can report 404
// for a resource that still exists. An Unparseable entry is an import
// failure. Resources holds parsed bodies only; Data is never nil.
type ReportResult struct {
	Resources   []Resource
	Missing     []ReportMiss
	Unparseable []ReportMiss
}

// ReportMiss names one href that a tolerant REPORT could not deliver. ETag
// is the ETag the response gave for the href, or "" when the response gave
// none (a top-level 404 has no propstat).
type ReportMiss struct {
	Href string
	ETag string
}

// reportProps is the DAV: prop payload of a calendar-multiget REPORT and of
// a calendar-query REPORT.
type reportProps struct {
	ETag         string `xml:"DAV: getetag"`
	CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
}

// decodeTolerantReport reads a multistatus body and sorts each response
// into Resources, Missing, or Unparseable.
//
// We cannot use go-webdav for this. Its decodeCalendarObjectList aborts the
// entire response when any single resource lacks the calendar-data
// property, or when one body does not parse. That is exactly what a 404'd
// row looks like. Real servers (Google in particular) routinely hand us
// hrefs in sync-collection that are gone a few hundred milliseconds later
// by multiget time, and a server can keep one broken body in a collection
// (issue #853). The intolerance then killed every pull of the calendar.
// Only an XML decode failure is fatal here.
func decodeTolerantReport(body io.Reader) (*ReportResult, error) {
	var ms davMultiStatus[reportProps]
	if err := xml.NewDecoder(body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("decode report response: %w", err)
	}

	result := &ReportResult{}
	for _, r := range ms.Responses {
		href := strings.TrimSpace(r.Href)
		if href == "" {
			continue
		}
		// Top-level <status>404</status> means the entire resource is gone.
		if topCode := parseStatusCode(r.Status); topCode == http.StatusNotFound {
			result.Missing = append(result.Missing, ReportMiss{Href: href})
			continue
		}
		// RFC 4918 section 9.1.2 lets a server split the properties of one
		// response across several 2xx propstats: getetag in one block and
		// calendar-data in another. Merge them before the classification.
		var rawETag, data string
		for _, ps := range allOKPropstats(r) {
			if rawETag == "" {
				rawETag = ps.ETag
			}
			if data == "" {
				data = ps.CalendarData
			}
		}
		etag := normalizeETag(rawETag)
		if data == "" {
			result.Missing = append(result.Missing, ReportMiss{Href: href, ETag: etag})
			continue
		}
		cal, parseErr := ical.NewDecoder(strings.NewReader(data)).Decode()
		if parseErr != nil {
			// The server returned a body we cannot parse. Report it instead
			// of an abort. The next sync revisits the href.
			result.Unparseable = append(result.Unparseable, ReportMiss{Href: href, ETag: etag})
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
