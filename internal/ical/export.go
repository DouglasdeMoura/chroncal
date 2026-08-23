package ical

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// ProductID is the PRODID value written into exported VCALENDAR objects.
// Override it before ExportEvents or ExportTodos to customise.
var ProductID = "-//chroncal//chroncal//EN"

// PrependComments inserts comment lines after the calendar's opening
// BEGIN:VCALENDAR line and returns the result. Each entry becomes one physical
// line that starts with "; ". A CR or LF inside an entry is replaced with a
// space, so one entry stays one line.
//
// RFC 5545 does not define a comment production, but readers skip unknown
// lines that start with "; " in practice, and chroncal's own importer strips
// them (see stripCommentLines). The CLI export --skip-unreadable path uses
// this to carry its caveat inside the file, so the file self-describes months
// later without terminal context. The data stays unchanged when it holds no
// VCALENDAR opening line.
func PrependComments(data []byte, comments []string) []byte {
	if len(comments) == 0 {
		return data
	}
	s := string(data)
	idx := strings.Index(s, "BEGIN:VCALENDAR")
	if idx < 0 {
		return data
	}
	eol := strings.IndexAny(s[idx:], "\r\n")
	var insertAt int
	if eol < 0 {
		// No line end after the opening line; append before whatever tail.
		insertAt = len(s)
	} else {
		insertAt = idx + eol
	}
	var b strings.Builder
	b.WriteString(s[:insertAt])
	repl := strings.NewReplacer("\r", " ", "\n", " ")
	for _, c := range comments {
		b.WriteString("\r\n; ")
		b.WriteString(repl.Replace(c))
	}
	b.WriteString(s[insertAt:])
	return []byte(b.String())
}

// MergeCalendars combines two iCal byte streams into one VCALENDAR.
// It takes the header from the first and appends all components from both.
func MergeCalendars(a, b []byte) []byte {
	// Simple approach: strip END:VCALENDAR from a, strip BEGIN:VCALENDAR...VERSION:2.0 header from b
	aStr := strings.TrimRight(string(a), "\r\n")
	bStr := string(b)

	// Remove trailing END:VCALENDAR from a
	if idx := strings.LastIndex(aStr, "END:VCALENDAR"); idx >= 0 {
		aStr = aStr[:idx]
	}

	// Extract VTIMEZONE blocks from b that are not already in a, so they
	// are preserved when the header of b is stripped below.
	var extraTZ string
	for _, tzBlock := range extractVTIMEZONEBlocks(bStr) {
		if !strings.Contains(aStr, tzBlock) {
			extraTZ += tzBlock
		}
	}

	// Remove header from b: find the earliest component marker regardless of
	// type, so that a stream mixing VEVENT/VTODO/VJOURNAL/VFREEBUSY does not
	// lose the components that appear before the first marker of a
	// later-searched type. VFREEBUSY is included so a free/busy-only stream is
	// still recognized; otherwise its BEGIN:VCALENDAR header would be appended
	// verbatim, nesting a second VCALENDAR.
	firstComp := -1
	for _, marker := range []string{"BEGIN:VEVENT", "BEGIN:VTODO", "BEGIN:VJOURNAL", "BEGIN:VFREEBUSY"} {
		if idx := strings.Index(bStr, marker); idx >= 0 {
			if firstComp < 0 || idx < firstComp {
				firstComp = idx
			}
		}
	}
	if firstComp < 0 {
		// b has no components to contribute; keep only the (already extracted)
		// VTIMEZONE blocks and avoid appending b's VCALENDAR header verbatim.
		return []byte(aStr + extraTZ + "END:VCALENDAR\r\n")
	}
	bStr = bStr[firstComp:]

	// Remove trailing END:VCALENDAR from b, then re-add it
	if idx := strings.LastIndex(bStr, "END:VCALENDAR"); idx >= 0 {
		bStr = bStr[:idx]
	}

	return []byte(aStr + extraTZ + bStr + "END:VCALENDAR\r\n")
}

// extractVTIMEZONEBlocks returns all BEGIN:VTIMEZONE...END:VTIMEZONE\r\n
// segments found in s.
func extractVTIMEZONEBlocks(s string) []string {
	var blocks []string
	rest := s
	for {
		start := strings.Index(rest, "BEGIN:VTIMEZONE")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start:], "END:VTIMEZONE")
		if end < 0 {
			break
		}
		// end is relative to start; include the "END:VTIMEZONE" tag plus a
		// trailing \r\n if present.
		blockEnd := start + end + len("END:VTIMEZONE")
		if blockEnd < len(rest) && rest[blockEnd] == '\r' {
			blockEnd++
		}
		if blockEnd < len(rest) && rest[blockEnd] == '\n' {
			blockEnd++
		}
		blocks = append(blocks, rest[start:blockEnd])
		rest = rest[blockEnd:]
	}
	return blocks
}

func emitDateListOnComponent(comp *ical.Component, propName, dates, timezone string) {
	if dates == "" {
		return
	}
	for _, ds := range strings.Split(dates, ",") {
		ds = strings.TrimSpace(ds)
		// Date-only values (YYYY-MM-DD) → emit as VALUE=DATE
		if t, err := time.Parse("2006-01-02", ds); err == nil {
			prop := &ical.Prop{Name: propName, Params: make(ical.Params)}
			prop.Params.Set("VALUE", "DATE")
			prop.Value = t.Format("20060102")
			comp.Props.Add(prop)
			continue
		}
		if t, err := time.Parse(time.RFC3339, ds); err == nil {
			prop := &ical.Prop{Name: propName, Params: make(ical.Params)}
			if timezone == "FLOATING" {
				// Floating components store wall-clock numbers; emit
				// EXDATE/RDATE as floating (no Z) so the value type matches
				// DTSTART. A trailing Z is a value-type mismatch that stops
				// servers from suppressing the excluded occurrence (#421).
				prop.Value = t.UTC().Format("20060102T150405")
			} else if loc, lerr := time.LoadLocation(timezone); timezone != "" && lerr == nil {
				// Zoned components emit DTSTART in local wall-clock with a
				// TZID, so EXDATE/RDATE must match: same TZID, same zone-local
				// wall clock. A bare UTC value drops the TZID and is a
				// value-type mismatch that stops servers expanding the RRULE
				// in the DTSTART zone (e.g. Google) from suppressing the
				// excluded occurrence (#492), mirroring setEventTimes.
				prop.Params.Set(ical.ParamTimezoneID, timezone)
				prop.Value = t.In(loc).Format("20060102T150405")
			} else {
				prop.SetDateTime(t.UTC())
			}
			comp.Props.Add(prop)
		}
	}
}

// emitRecurrenceID writes RECURRENCE-ID onto props. recurrenceID is the stored
// RFC 3339 string. Per RFC 5545 §3.8.4.4 the RECURRENCE-ID value type must
// match the master's DTSTART. When the component is all-day it is emitted as
// VALUE=DATE (YYYYMMDD). When floating (no timezone) it is emitted as a
// floating DATE-TIME (no Z, no TZID). Otherwise it is a UTC DATE-TIME. A type
// mismatch prevents CalDAV servers from a bind of the override to its master.
func emitRecurrenceID(props ical.Props, recurrenceID string, allDay, floating bool) {
	t, err := time.Parse(time.RFC3339, recurrenceID)
	if err != nil {
		return
	}
	switch {
	case allDay:
		props.SetDate(ical.PropRecurrenceID, t.UTC())
	case floating:
		props.Set(&ical.Prop{
			Name:  ical.PropRecurrenceID,
			Value: t.UTC().Format("20060102T150405"),
		})
	default:
		props.SetDateTime(ical.PropRecurrenceID, t.UTC())
	}
}

// setAttendeeParams adds RFC 5545 ATTENDEE parameters beyond the base CN/PARTSTAT/ROLE.
func setAttendeeParams(prop *ical.Prop, att model.Attendee) {
	if att.CUType != "" && att.CUType != "INDIVIDUAL" {
		prop.Params.Set(ical.ParamCalendarUserType, att.CUType)
	}
	if att.RSVPRequested {
		prop.Params.Set(ical.ParamRSVP, "TRUE")
	}
	if att.SentBy != "" {
		prop.Params.Set(ical.ParamSentBy, "mailto:"+att.SentBy)
	}
	for _, v := range splitNonEmpty(att.DelegatedTo) {
		prop.Params.Add(ical.ParamDelegatedTo, "mailto:"+v)
	}
	for _, v := range splitNonEmpty(att.DelegatedFrom) {
		prop.Params.Add(ical.ParamDelegatedFrom, "mailto:"+v)
	}
	for _, v := range splitNonEmpty(att.Member) {
		prop.Params.Add(ical.ParamMember, "mailto:"+v)
	}
	if att.Dir != "" {
		prop.Params.Set(ical.ParamDir, att.Dir)
	}
	if att.Language != "" {
		prop.Params.Set(ical.ParamLanguage, att.Language)
	}
}

// setOrganizerParams adds applicable RFC 5545 parameters to an ORGANIZER property.
func setOrganizerParams(prop *ical.Prop, att model.Attendee) {
	if att.SentBy != "" {
		prop.Params.Set(ical.ParamSentBy, "mailto:"+att.SentBy)
	}
	if att.Dir != "" {
		prop.Params.Set(ical.ParamDir, att.Dir)
	}
	if att.Language != "" {
		prop.Params.Set(ical.ParamLanguage, att.Language)
	}
}

// splitNonEmpty splits a comma-separated string and returns non-empty trimmed values.
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// propFromXProperty converts one stored X-property into a wire property,
// decoding its JSON-serialized parameters. The DTEND override in setEventTimes
// and emitXProperties share it, so the wire encoding of the stored params has
// one definition.
func propFromXProperty(xp model.XProperty) *ical.Prop {
	p := &ical.Prop{Name: xp.Name, Params: make(ical.Params)}
	p.Value = xp.Value
	if xp.Params != "" && xp.Params != "{}" {
		var params map[string][]string
		if err := json.Unmarshal([]byte(xp.Params), &params); err == nil {
			for k, vals := range params {
				for _, v := range vals {
					p.Params.Add(k, v)
				}
			}
		}
	}
	return p
}

// emitXProperties writes X-properties (and other unhandled properties) onto an
// iCal component for round-trip preservation. libical-internal annotations
// (X-LIC-ERROR / X-LIC-ERRORTYPE) are skipped. Those are diagnostic markers
// that libical emitted when it encountered a parse error in the original
// payload. They are not real properties. An echo of them back to a CalDAV
// server (Google in particular) gets the whole resource rejected with HTTP 400.
func emitXProperties(comp *ical.Component, xprops []model.XProperty) {
	for _, xp := range xprops {
		if isLibicalDiagnosticProp(xp.Name) {
			continue
		}
		// The original-DTEND slot supplies the DTEND override in
		// setEventTimes. An emit here would duplicate the value on the
		// wire.
		if xp.Name == xpropOriginalDTEND {
			continue
		}
		comp.Props.Add(propFromXProperty(xp))
	}
}

// unsafeParamBytes strips the bytes the iCal parameter grammar cannot
// carry. The go-ical encoder rejects a parameter value that contains a
// double-quote. A CR or LF would split the property line.
var unsafeParamBytes = strings.NewReplacer(`"`, "", "\r", "", "\n", "")

// safeParamValue makes a free-text value safe for an iCal parameter. The
// export paths use it for CN, FILENAME, and FMTTYPE. One unsafe stored
// value must not fail the whole export batch.
func safeParamValue(v string) string {
	return unsafeParamBytes.Replace(v)
}

func emitAttachment(props ical.Props, att model.Attachment) {
	p := &ical.Prop{Name: ical.PropAttach, Params: make(ical.Params)}
	if att.Data != nil {
		// Inline binary attachment
		p.Value = base64.StdEncoding.EncodeToString(att.Data)
		p.Params.Set("ENCODING", "BASE64")
		p.Params.Set("VALUE", "BINARY")
		if att.Filename != "" {
			p.Params.Set("FILENAME", safeParamValue(att.Filename))
		}
	} else {
		p.Value = att.URI
	}
	if att.FmtType != "" {
		p.Params.Set("FMTTYPE", safeParamValue(att.FmtType))
	}
	props.Add(p)
}

// emitAttendees writes the ORGANIZER and ATTENDEE properties for one
// component. The organizer uses Set (one per component); attendees append.
// Text in the CN parameter passes through safeParamValue, because the
// encoder rejects parameter values that carry a double quote.
func emitAttendees(props ical.Props, attendees []model.Attendee) {
	for _, att := range attendees {
		if att.Organizer {
			org := &ical.Prop{Name: ical.PropOrganizer, Params: make(ical.Params)}
			org.Value = "mailto:" + att.Email
			if att.Name != "" {
				org.Params.Set(ical.ParamCommonName, safeParamValue(att.Name))
			}
			setOrganizerParams(org, att)
			props.Set(org)
		}

		attendee := &ical.Prop{Name: ical.PropAttendee, Params: make(ical.Params)}
		attendee.Value = "mailto:" + att.Email
		if att.Name != "" {
			attendee.Params.Set(ical.ParamCommonName, safeParamValue(att.Name))
		}
		attendee.Params.Set(ical.ParamParticipationStatus, att.RSVPStatus)
		attendee.Params.Set(ical.ParamRole, att.Role)
		setAttendeeParams(attendee, att)
		props.Add(attendee)
	}
}

// emitTimestamps writes DTSTAMP, CREATED, and LAST-MODIFIED. A parseable
// dtstamp wins; otherwise the updated timestamp stands in for DTSTAMP.
func emitTimestamps(props ical.Props, dtstamp string, createdAt, updatedAt time.Time) {
	if ts, err := time.Parse(time.RFC3339, dtstamp); err == nil {
		props.SetDateTime(ical.PropDateTimeStamp, ts.UTC())
	} else {
		props.SetDateTime(ical.PropDateTimeStamp, updatedAt.UTC())
	}
	props.SetDateTime(ical.PropCreated, createdAt.UTC())
	props.SetDateTime(ical.PropLastModified, updatedAt.UTC())
}

// emitTextProps appends one TEXT property per value (COMMENT, CONTACT).
func emitTextProps(props ical.Props, name string, values []string) {
	for _, c := range values {
		p := &ical.Prop{Name: name}
		p.SetText(c)
		props.Add(p)
	}
}

// emitResources writes the comma-separated RESOURCES list property. It skips
// an empty list, because a bare RESOURCES carries no information.
func emitResources(props ical.Props, resources []string) {
	if len(resources) > 0 {
		resProp := &ical.Prop{Name: ical.PropResources}
		resProp.SetTextList(resources)
		props.Set(resProp)
	}
}

// emitRelations appends one RELATED-TO property per relation. The RELTYPE
// parameter is omitted for the default PARENT type.
func emitRelations(props ical.Props, relations []model.Relation) {
	for _, r := range relations {
		p := &ical.Prop{Name: ical.PropRelatedTo, Params: make(ical.Params)}
		p.Value = r.RelUID
		if r.RelType != "" && r.RelType != "PARENT" {
			p.Params.Set("RELTYPE", r.RelType)
		}
		props.Add(p)
	}
}

// setZonedDateTime writes one datetime property under the shared timezone
// rules. A "FLOATING" timezone emits bare wall-clock numbers (no Z, no TZID),
// matching the stored floating semantics. A resolvable IANA timezone emits
// the zone-local wall clock plus the TZID parameter. An empty or
// unresolvable timezone falls back to UTC.
func setZonedDateTime(props ical.Props, name string, t time.Time, timezone string) {
	if timezone == "FLOATING" {
		p := &ical.Prop{Name: name}
		p.Value = t.UTC().Format("20060102T150405")
		props.Set(p)
		return
	}
	if loc, err := time.LoadLocation(timezone); timezone != "" && err == nil {
		props.SetDateTime(name, t.In(loc))
		if p := props.Get(name); p != nil {
			p.Params.Set(ical.ParamTimezoneID, timezone)
		}
		return
	}
	props.SetDateTime(name, t.UTC())
}
