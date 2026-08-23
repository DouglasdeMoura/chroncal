package caldav

import "encoding/xml"

// This file carries the one shared WebDAV multistatus envelope. Every
// REPORT/PROPFIND response decodes through it. Before the consolidation, four
// packages-private struct hierarchies restated the same
// multistatus → response → propstat shape with per-call-site Prop payloads,
// and drifted independently.
//
// A call site defines only its Prop payload type and instantiates the generic
// envelope with it.

// davMultiStatus decodes a DAV: multistatus document. P is the call site's
// expected DAV: prop payload.
type davMultiStatus[P any] struct {
	XMLName   xml.Name         `xml:"DAV: multistatus"`
	Responses []davResponse[P] `xml:"DAV: response"`
	// SyncToken is present only in sync-collection responses; other reports
	// leave it empty.
	SyncToken string `xml:"DAV: sync-token"`
}

// davResponse is one DAV: response element.
type davResponse[P any] struct {
	Href      string           `xml:"DAV: href"`
	Status    string           `xml:"DAV: status"`
	PropStats []davPropStat[P] `xml:"DAV: propstat"`
}

// davPropStat pairs a status line with its property payload.
type davPropStat[P any] struct {
	Status string `xml:"DAV: status"`
	Prop   P      `xml:"DAV: prop"`
}

// firstOKPropstat returns the first 2xx propstat of a response. ok is false
// when the response carries no 2xx propstat.
func firstOKPropstat[P any](r davResponse[P]) (P, bool) {
	for _, ps := range r.PropStats {
		if code := parseStatusCode(ps.Status); code >= 200 && code < 300 {
			return ps.Prop, true
		}
	}
	var zero P
	return zero, false
}
