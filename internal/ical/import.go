package ical

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/freebusy"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TimezoneData holds a serialized VTIMEZONE component extracted during import.
type TimezoneData struct {
	TZID string
	Data string // serialized VTIMEZONE component
}

type ImportResult struct {
	Events    []event.Event
	Todos     []todo.Todo
	Journals  []journal.Journal
	FreeBusy  []freebusy.Result
	Timezones []TimezoneData
	Warnings  []string
	// SkippedComponents counts VEVENT/VTODO/VJOURNAL components that failed
	// to parse and were dropped (each also recorded in Warnings). Non-zero
	// means Events/Todos/Journals is an incomplete inventory of the input, so
	// absence-based reconciliation (e.g. override pruning) is unsafe.
	SkippedComponents int
}

const (
	maxImportBytes           = 8 << 20
	maxInlineAttachmentBytes = 1 << 20
)

var errImportLimitExceeded = errors.New("ical import exceeds configured limits")

// xpropOriginalDTEND preserves a server DTEND that failed to parse. Export
// emits the stored string as DTEND, so the fabricated local span does not
// overwrite the server value (issue #567). A local edit that changes the
// span clears the slot first (see internal/event, issue #649).
//
// Only the CalDAV pull path sets the slot. A file import did not receive
// the value from the target server. An export of such a value could send
// the target server a DTEND it rejects, and the resource then stays dirty.
const xpropOriginalDTEND = model.XPropOriginalDTEND

// ImportFile parses an iCal stream from a file or another local source.

// stripCommentLines removes physical lines that start with ";" and returns the
// rest. RFC 5545 does not define a comment production, but chroncal's own
// ical export --skip-unreadable writes a caveat header in this shape, so the
// importer must accept its own output again. A folded continuation line starts
// with a space or a tab, so a ";" at column 0 can only be a comment line.
func stripCommentLines(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte(";")) && !bytes.Contains(data, []byte("\n;")) {
		return data
	}
	var out []byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if len(line) > 0 && line[0] == ';' {
			continue
		}
		out = append(out, line...)
	}
	return out
}
func ImportFile(r io.Reader) (ImportResult, error) {
	return importFile(r, false)
}

// ImportFileRemote parses an iCal stream that a CalDAV server served. A
// DTEND that fails to parse is stored verbatim in
// X-CHRONCAL-ORIGINAL-DTEND. Export then returns the exact server value
// (issue #567).
func ImportFileRemote(r io.Reader) (ImportResult, error) {
	return importFile(r, true)
}

func importFile(r io.Reader, remote bool) (ImportResult, error) {
	var result ImportResult
	// skipComponent is the one place that defines what "the parser dropped a
	// persistable component" means: the warning for the user plus the count
	// that disables absence-based reconciliation downstream (see
	// SkippedComponents). Every VEVENT/VTODO/VJOURNAL failure path must go
	// through it.
	skipComponent := func(kind string, err error) {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", kind, err))
		result.SkippedComponents++
	}
	data, err := io.ReadAll(io.LimitReader(r, maxImportBytes+1))
	if err != nil {
		return result, fmt.Errorf("read ical: %w", err)
	}
	if len(data) > maxImportBytes {
		return result, fmt.Errorf("ical payload exceeds %d bytes", maxImportBytes)
	}
	data = stripCommentLines(data)

	dec := ical.NewDecoder(bytes.NewReader(data))

	for {
		cal, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, fmt.Errorf("decode ical: %w", err)
		}

		// Build timezone map from VTIMEZONE components.
		tzMap := buildTZMap(cal)

		// Extract and serialize VTIMEZONE components for storage.
		for _, child := range cal.Children {
			if child.Name != ical.CompTimezone {
				continue
			}
			tzid := compPropText(child, ical.PropTimezoneID)
			if tzid == "" {
				continue
			}
			var buf bytes.Buffer
			enc := ical.NewEncoder(&buf)
			// Wrap in a minimal calendar for encoding. go-ical's encoder
			// rejects a VCALENDAR that is missing the mandatory PRODID and
			// VERSION properties, so set them; otherwise Encode fails and the
			// VTIMEZONE block is silently dropped from result.Timezones.
			tmpCal := ical.NewCalendar()
			tmpCal.Props.SetText(ical.PropVersion, "2.0")
			tmpCal.Props.SetText(ical.PropProductID, ProductID)
			tmpCal.Children = append(tmpCal.Children, child)
			if err := enc.Encode(tmpCal); err == nil {
				// Extract just the VTIMEZONE block from the encoded output.
				encoded := buf.String()
				if start := strings.Index(encoded, "BEGIN:VTIMEZONE"); start >= 0 {
					if end := strings.Index(encoded[start:], "END:VTIMEZONE"); end >= 0 {
						vtData := encoded[start : start+end+len("END:VTIMEZONE\r\n")]
						result.Timezones = append(result.Timezones, TimezoneData{
							TZID: tzid,
							Data: vtData,
						})
					}
				}
			}
		}

		skipped := make(map[string]int)
		for _, child := range cal.Children {
			switch child.Name {
			case ical.CompEvent:
				vevent := ical.Event{Component: child}
				resolveComponentTZIDs(child, tzMap)
				e, warns, err := eventFromVEvent(vevent, remote)
				if err != nil {
					if errors.Is(err, errImportLimitExceeded) {
						return result, err
					}
					skipComponent("VEVENT", err)
					continue
				}
				result.Warnings = append(result.Warnings, warns...)
				result.Events = append(result.Events, e)
			case ical.CompToDo:
				resolveComponentTZIDs(child, tzMap)
				t, warns, err := todoFromVTodo(child)
				if err != nil {
					if errors.Is(err, errImportLimitExceeded) {
						return result, err
					}
					skipComponent("VTODO", err)
					continue
				}
				result.Warnings = append(result.Warnings, warns...)
				result.Todos = append(result.Todos, t)
			case ical.CompJournal:
				resolveComponentTZIDs(child, tzMap)
				j, warns, err := journalFromVJournal(child)
				if err != nil {
					if errors.Is(err, errImportLimitExceeded) {
						return result, err
					}
					skipComponent("VJOURNAL", err)
					continue
				}
				result.Warnings = append(result.Warnings, warns...)
				result.Journals = append(result.Journals, j)
			case ical.CompFreeBusy:
				resolveComponentTZIDs(child, tzMap)
				fb, err := freebusy.ParseComponent(child)
				if err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("VFREEBUSY: %v", err))
					continue
				}
				result.FreeBusy = append(result.FreeBusy, fb)
			default:
				if child.Name != "VTIMEZONE" {
					skipped[child.Name]++
				}
			}
		}
		for name, count := range skipped {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped: %s (%d)", name, count))
		}
	}

	return result, nil
}

// parseDateProp formats a component's date/date-time property for storage.
// It returns a warning rather than a silent discard of an unparseable value.
// A dropped DUE or DTSTART is invisible data loss. The record disappears from
// date views. Any alarms lose their anchor. The next export re-emits the
// component with no property. An empty first return means the
// property was absent (no warning) or unusable (warning set).
func parseDateProp(props ical.Props, kind, name, uid string) (string, string) {
	prop := props.Get(name)
	if prop == nil {
		return "", ""
	}
	t, err := prop.DateTime(nil)
	if err != nil || t.IsZero() {
		return "", fmt.Sprintf("%s %q: unparseable %s %q; dropped", kind, uid, name, prop.Value)
	}
	// A bare date (VALUE=DATE, "YYYYMMDD") is a calendar date, not an instant.
	if len(prop.Value) == 8 {
		return t.Format("2006-01-02"), ""
	}
	return t.UTC().Format(time.RFC3339), ""
}

// parseAttendees reads the attendees of a VEVENT. The second return
// value lists the parameters attendeeFromProp clamped.
func parseAttendees(ve ical.Event) ([]model.Attendee, []string) {
	var attendees []model.Attendee
	var warns []string

	// ORGANIZER — track email so we can deduplicate against ATTENDEE below.
	var organizerEmail string
	if prop := ve.Props.Get(ical.PropOrganizer); prop != nil {
		organizerEmail = stripMailto(prop.Value)
		attendees = append(attendees, model.Attendee{
			Email:      organizerEmail,
			Name:       prop.Params.Get(ical.ParamCommonName),
			RSVPStatus: "ACCEPTED",
			Role:       "CHAIR",
			Organizer:  true,
			SentBy:     stripMailto(prop.Params.Get(ical.ParamSentBy)),
			Dir:        prop.Params.Get(ical.ParamDir),
			Language:   prop.Params.Get(ical.ParamLanguage),
		})
	}

	// ATTENDEE properties — skip duplicates of the ORGANIZER email.
	for _, prop := range ve.Props.Values(ical.PropAttendee) {
		email := stripMailto(prop.Value)
		if organizerEmail != "" && strings.EqualFold(email, organizerEmail) {
			continue
		}
		a, w := attendeeFromProp(&prop, model.EventAttendee)
		warns = append(warns, w...)
		attendees = append(attendees, a)
	}

	return attendees, warns
}

func stripMailto(s string) string {
	return strings.TrimPrefix(strings.TrimPrefix(s, "mailto:"), "MAILTO:")
}

func paramOrDefault(prop *ical.Prop, param, def string) string {
	if v := prop.Params.Get(param); v != "" {
		return v
	}
	return def
}

// Props-based helpers for VTODO (no wrapper type in go-ical)

func propText(props ical.Props, name string) string {
	if prop := props.Get(name); prop != nil {
		if v, err := prop.Text(); err == nil {
			return v
		}
		return prop.Value
	}
	return ""
}

func propTextOr(props ical.Props, name, def string) string {
	if v := propText(props, name); v != "" {
		return v
	}
	return def
}

// parseRecurrenceID extracts a RECURRENCE-ID as an RFC 3339 UTC string, or ""
// when the property is absent. A present-but-unparseable value fails the
// component. A degrade to "" would import the override as the series
// master. That would clobber the real master row and corrupt override
// reconciliation.
func parseRecurrenceID(props ical.Props) (string, error) {
	prop := props.Get(ical.PropRecurrenceID)
	if prop == nil {
		return "", nil
	}
	t, err := prop.DateTime(nil)
	if err != nil || t.IsZero() {
		return "", fmt.Errorf("unparseable RECURRENCE-ID %q", prop.Value)
	}
	return t.UTC().Format(time.RFC3339), nil
}

func parseCategoriesFromProps(props ical.Props) string {
	var cats []string
	for _, prop := range props.Values(ical.PropCategories) {
		if list, err := prop.TextList(); err == nil {
			cats = append(cats, list...)
		}
	}
	return timeutil.JoinCategoryList(cats)
}

// dtstartZone resolves the component's anchor zone for TZID-less DATE-TIME
// values elsewhere in the component. The anchor is the IANA TZID on DTSTART
// (or DUE for a VTODO with no DTSTART). Returns nil when the anchor is absent,
// floating, UTC, or date-only. Those components have no zone to inherit.
// TZID-less values then keep their UTC/floating read.
func dtstartZone(props ical.Props) *time.Location {
	for _, name := range []string{ical.PropDateTimeStart, ical.PropDue} {
		prop := props.Get(name)
		if prop == nil {
			continue
		}
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			if loc, err := time.LoadLocation(tzid); err == nil {
				return loc
			}
		}
		return nil
	}
	return nil
}

func parseDateListFromProps(props ical.Props, propName string, fallback *time.Location) string {
	var dates []string
	for _, prop := range props.Values(propName) {
		var loc *time.Location
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			if loaded, err := time.LoadLocation(tzid); err == nil {
				loc = loaded
			} else {
				// Unresolvable TZID (a private or Windows name that survived
				// resolveComponentTZIDs): reading the wall clock as UTC skews
				// the instant by the zone offset, so the EXDATE never matches
				// the occurrence it excludes and a server-cancelled instance
				// keeps rendering locally. The component's DTSTART zone is the
				// best-effort locality.
				loc = fallback
			}
		} else {
			// No TZID: a floating local time (RFC 5545 §3.3.5). For a
			// zone-anchored component the wall clock is local to the DTSTART
			// zone (Google transiently emits EXDATEs in this form), not UTC.
			// Z-suffixed values never reach the loc branch — ParseInLocation
			// rejects the trailing Z — and keep their UTC reading below.
			loc = fallback
		}
		parts := strings.Split(prop.Value, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// RDATE;VALUE=PERIOD values are "start/end" or "start/duration"
			// (RFC 5545 §3.8.5.2). Keep the start instant; the period's
			// duration is not represented in the comma-separated RDATE store.
			if i := strings.IndexByte(p, '/'); i >= 0 {
				p = p[:i]
			}
			if strings.EqualFold(prop.Params.Get("VALUE"), "DATE") {
				if t, err := time.Parse("20060102", p); err == nil {
					dates = append(dates, t.Format("2006-01-02"))
				}
				continue
			}
			if loc != nil {
				if t, err := time.ParseInLocation("20060102T150405", p, loc); err == nil {
					dates = append(dates, t.UTC().Format(time.RFC3339))
					continue
				}
			}
			for _, layout := range []string{
				"20060102T150405Z",
				"20060102T150405",
				"20060102",
				time.RFC3339,
			} {
				if t, err := time.Parse(layout, p); err == nil {
					// Preserve date-only format for VALUE=DATE
					if layout == "20060102" {
						dates = append(dates, t.Format("2006-01-02"))
					} else {
						dates = append(dates, t.UTC().Format(time.RFC3339))
					}
					break
				}
			}
		}
	}
	return strings.Join(dates, ",")
}

// parseAttendeesFromProps reads the attendees of a VTODO or a VJOURNAL.
// Those two tables accept the wider PARTSTAT set, so the caller passes
// model.TaskAttendee. The second return value lists the clamps.
func parseAttendeesFromProps(props ical.Props, kind model.AttendeeKind) ([]model.Attendee, []string) {
	var attendees []model.Attendee
	var warns []string

	var organizerEmail string
	if prop := props.Get(ical.PropOrganizer); prop != nil {
		organizerEmail = stripMailto(prop.Value)
		attendees = append(attendees, model.Attendee{
			Email:      organizerEmail,
			Name:       prop.Params.Get(ical.ParamCommonName),
			RSVPStatus: "ACCEPTED",
			Role:       "CHAIR",
			Organizer:  true,
			SentBy:     stripMailto(prop.Params.Get(ical.ParamSentBy)),
			Dir:        prop.Params.Get(ical.ParamDir),
			Language:   prop.Params.Get(ical.ParamLanguage),
		})
	}

	for _, prop := range props.Values(ical.PropAttendee) {
		email := stripMailto(prop.Value)
		if organizerEmail != "" && strings.EqualFold(email, organizerEmail) {
			continue
		}
		a, w := attendeeFromProp(&prop, kind)
		warns = append(warns, w...)
		attendees = append(attendees, a)
	}

	return attendees, warns
}

// attendeeFromProp extracts a model.Attendee from an iCal ATTENDEE
// property. It keeps every RFC 5545 parameter. It clamps the three
// parameters the attendee tables constrain. RFC 5545 permits an x-name or an
// iana-token for PARTSTAT (§3.2.12), ROLE (§3.2.16), and CUTYPE
// (§3.2.3), but each column carries a CHECK constraint. A stored
// out-of-set value would fail the insert inside the sync transaction and
// roll back the whole resource on every pass (issue #587). The second
// return value lists the clamps, so the caller reports them.
func attendeeFromProp(prop *ical.Prop, kind model.AttendeeKind) (model.Attendee, []string) {
	var warns []string
	partstat := clampAttendeeParam(&warns, "PARTSTAT",
		strings.ToUpper(paramOrDefault(prop, ical.ParamParticipationStatus, model.DefaultRSVPStatus)),
		model.DefaultRSVPStatus,
		func(v string) bool { return model.ValidRSVPStatus(kind, v) })
	role := clampAttendeeParam(&warns, "ROLE",
		strings.ToUpper(paramOrDefault(prop, ical.ParamRole, model.DefaultAttendeeRole)),
		model.DefaultAttendeeRole,
		model.ValidAttendeeRole)
	// RFC 5545 §3.2.3 tells a reader to treat a CUTYPE it does not know
	// the same way as UNKNOWN, so the clamp keeps that meaning.
	cutype := clampAttendeeParam(&warns, "CUTYPE",
		strings.ToUpper(paramOrDefault(prop, ical.ParamCalendarUserType, "INDIVIDUAL")),
		model.UnknownCUType,
		model.ValidAttendeeCUType)

	return model.Attendee{
		Email:         stripMailto(prop.Value),
		Name:          prop.Params.Get(ical.ParamCommonName),
		RSVPStatus:    partstat,
		Role:          role,
		CUType:        cutype,
		RSVPRequested: strings.EqualFold(prop.Params.Get(ical.ParamRSVP), "TRUE"),
		SentBy:        stripMailto(prop.Params.Get(ical.ParamSentBy)),
		DelegatedTo:   joinMailtoParams(prop.Params.Values(ical.ParamDelegatedTo)),
		DelegatedFrom: joinMailtoParams(prop.Params.Values(ical.ParamDelegatedFrom)),
		Member:        joinMailtoParams(prop.Params.Values(ical.ParamMember)),
		Dir:           prop.Params.Get(ical.ParamDir),
		Language:      prop.Params.Get(ical.ParamLanguage),
	}, warns
}

// clampAttendeeParam returns value when valid reports it. Otherwise it
// records a warning and returns fallback.
func clampAttendeeParam(warns *[]string, param, value, fallback string, valid func(string) bool) string {
	if valid(value) {
		return value
	}
	*warns = append(*warns, fmt.Sprintf("ATTENDEE %s=%q: unsupported value, using %s", param, value, fallback))
	return fallback
}

// joinMailtoParams joins multiple mailto URI param values into a comma-separated
// string. It strips the "mailto:" prefix and quotes around each value.
func joinMailtoParams(values []string) string {
	if len(values) == 0 {
		return ""
	}
	cleaned := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.Trim(v, "\"")
		v = stripMailto(v)
		if v != "" {
			cleaned = append(cleaned, v)
		}
	}
	return strings.Join(cleaned, ",")
}

// floatingOrTZ returns "FLOATING" if the time was detected as floating,
// otherwise returns the original timezone string.
func floatingOrTZ(floating bool, tz string) string {
	if floating {
		return "FLOATING"
	}
	return tz
}

// addDuration parses an RFC 5545 duration string and adds it to a time.
// Format: [+/-]P[nW] or [+/-]P[nD][T[nH][nM][nS]]
func addDuration(t time.Time, dur string) time.Time {
	return duration.Add(t, dur)
}

// decodeInlineAttachment decodes a BASE64 ATTACH value and enforces the inline
// size limit. The length is checked before decode so a deliberately oversized
// payload is rejected without a large buffer allocation.
func decodeInlineAttachment(value string) ([]byte, error) {
	if base64.StdEncoding.DecodedLen(len(value)) > maxInlineAttachmentBytes {
		return nil, fmt.Errorf("%w: inline attachment exceeds %d bytes", errImportLimitExceeded, maxInlineAttachmentBytes)
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode inline attachment: %w", err)
	}
	if len(data) > maxInlineAttachmentBytes {
		return nil, fmt.Errorf("%w: inline attachment exceeds %d bytes", errImportLimitExceeded, maxInlineAttachmentBytes)
	}
	return data, nil
}

func parseAttachmentsFromProps(props ical.Props) ([]model.Attachment, error) {
	var out []model.Attachment
	for _, prop := range props.Values(ical.PropAttach) {
		fmttype := prop.Params.Get("FMTTYPE")
		if prop.Params.Get("ENCODING") == "BASE64" {
			data, err := decodeInlineAttachment(prop.Value)
			if err != nil {
				return nil, err
			}
			out = append(out, model.Attachment{
				FmtType:  fmttype,
				Data:     data,
				Filename: prop.Params.Get("FILENAME"),
			})
		} else {
			out = append(out, model.Attachment{
				URI:     prop.Value,
				FmtType: fmttype,
			})
		}
	}
	return out, nil
}

func parseCommentsFromProps(props ical.Props) []string {
	var out []string
	for _, prop := range props.Values(ical.PropComment) {
		text, err := prop.Text()
		if err != nil {
			text = prop.Value
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func parseContactsFromProps(props ical.Props) []string {
	var out []string
	for _, prop := range props.Values(ical.PropContact) {
		text, err := prop.Text()
		if err != nil {
			text = prop.Value
		}
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func parseResourcesFromProps(props ical.Props) []string {
	var out []string
	for _, prop := range props.Values(ical.PropResources) {
		// RESOURCES is a comma-separated list (like CATEGORIES).
		// Use TextList to split on unescaped commas correctly.
		if list, err := prop.TextList(); err == nil {
			for _, s := range list {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func parseRelationsFromProps(props ical.Props) []model.Relation {
	var out []model.Relation
	for _, prop := range props.Values(ical.PropRelatedTo) {
		relType := prop.Params.Get("RELTYPE")
		if relType == "" {
			relType = "PARENT" // default per RFC 5545
		}
		if prop.Value != "" {
			out = append(out, model.Relation{
				RelType: strings.ToUpper(relType),
				RelUID:  prop.Value,
			})
		}
	}
	return out
}

// extractXPropertiesWithSet collects properties not in the handled set.
// If handled is nil, only X-* prefixed properties are captured.
func extractXPropertiesWithSet(props ical.Props, handled map[string]bool) []model.XProperty {
	var out []model.XProperty

	// Iterate property names in sorted order for deterministic output.
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		isXProp := strings.HasPrefix(name, "X-")
		if !isXProp && (handled == nil || handled[name]) {
			continue
		}
		if isLibicalDiagnosticProp(name) {
			continue
		}
		for _, prop := range props[name] {
			params := "{}"
			if len(prop.Params) > 0 {
				if b, err := json.Marshal(prop.Params); err == nil {
					params = string(b)
				}
			}
			out = append(out, model.XProperty{
				Name:   name,
				Value:  prop.Value,
				Params: params,
			})
		}
	}
	return out
}

// isLibicalDiagnosticProp reports whether an X-property name is a libical
// internal diagnostic marker. libical writes X-LIC-ERROR / X-LIC-ERRORTYPE
// directly into the parsed property bag whenever it gives up on a malformed
// property. They look like X-properties to consumers but are really
// parser-state. They round-trip into our DB on import and back onto the
// wire on export. Strict CalDAV servers (Google) then reject the resource
// with HTTP 400. Drop them at both ends so they never reach the server.
func isLibicalDiagnosticProp(name string) bool {
	return strings.HasPrefix(name, "X-LIC-")
}
