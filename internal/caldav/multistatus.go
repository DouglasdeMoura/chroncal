package caldav

import "encoding/xml"

// This file carries the single shared WebDAV multistatus envelope. The
// package decodes every REPORT and PROPFIND response through this envelope.
// A call site defines only its Prop payload type and instantiates the
// generic envelope with it. The envelope owns the multistatus, response,
// and propstat shape, so all call sites decode that shape in the same way.

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

// allOKPropstats returns the prop payload of every 2xx propstat, in
// document order. RFC 4918 §9.1.2 lets a server split the properties of one
// response across several 2xx propstats, so a caller that merges fields
// iterates all of them. The slice is empty when the response carries no 2xx
// propstat. A caller that needs only the first 2xx propstat uses
// firstOKPropstat instead.
func allOKPropstats[P any](r davResponse[P]) []P {
	var props []P
	for _, ps := range r.PropStats {
		if code := parseStatusCode(ps.Status); code >= 200 && code < 300 {
			props = append(props, ps.Prop)
		}
	}
	return props
}
