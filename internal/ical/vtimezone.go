package ical

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	goical "github.com/emersion/go-ical"
)

// buildTZMap extracts VTIMEZONE components from a calendar and builds a map
// from TZID to *time.Location. For each VTIMEZONE, the most recent STANDARD
// sub-component's TZOFFSETTO is used to create a fixed-offset location.
// If the TZID is already a valid IANA name, it is resolved via
// time.LoadLocation instead.
func buildTZMap(cal *goical.Calendar) map[string]*time.Location {
	m := make(map[string]*time.Location)
	for _, child := range cal.Children {
		if child.Name != goical.CompTimezone {
			continue
		}
		tzid := compPropText(child, goical.PropTimezoneID)
		if tzid == "" {
			continue
		}

		// Try IANA first — handles cases where TZID is valid but wrapped
		// in a VTIMEZONE we don't need to parse.
		if loc, err := time.LoadLocation(tzid); err == nil {
			m[tzid] = loc
			continue
		}

		// Try Windows-to-IANA mapping.
		if iana, ok := windowsToIANA[tzid]; ok {
			if loc, err := time.LoadLocation(iana); err == nil {
				m[tzid] = loc
				continue
			}
		}

		// Fall back to extracting fixed offset from STANDARD sub-component.
		if loc := locationFromVTZ(child, tzid); loc != nil {
			m[tzid] = loc
		}
	}
	return m
}

// locationFromVTZ extracts a fixed-offset *time.Location from a VTIMEZONE
// component by reading the TZOFFSETTO of the most recent STANDARD
// sub-component (or DAYLIGHT if no STANDARD exists).
func locationFromVTZ(vtz *goical.Component, tzid string) *time.Location {
	var offset string
	// Prefer STANDARD; fall back to DAYLIGHT.
	for _, sub := range vtz.Children {
		if sub.Name == goical.CompTimezoneStandard {
			if v := compPropText(sub, goical.PropTimezoneOffsetTo); v != "" {
				offset = v
			}
		}
	}
	if offset == "" {
		for _, sub := range vtz.Children {
			if sub.Name == goical.CompTimezoneDaylight {
				if v := compPropText(sub, goical.PropTimezoneOffsetTo); v != "" {
					offset = v
				}
			}
		}
	}
	if offset == "" {
		return nil
	}

	secs, err := parseUTCOffset(offset)
	if err != nil {
		return nil
	}
	return time.FixedZone(tzid, secs)
}

func compPropText(c *goical.Component, name string) string {
	if p := c.Props.Get(name); p != nil {
		return p.Value
	}
	return ""
}

// parseUTCOffset parses an RFC 5545 UTC-OFFSET value like "+0530", "-0800",
// or "+005258" (the optional trailing seconds component) and returns the
// offset in seconds.
func parseUTCOffset(s string) (int, error) {
	s = strings.TrimSpace(s)
	if len(s) < 5 {
		return 0, fmt.Errorf("utc-offset too short: %q", s)
	}
	sign := 1
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign = -1
		s = s[1:]
	}
	// After the sign, RFC 5545 allows "HHMM" or "HHMMSS".
	if len(s) != 4 && len(s) != 6 {
		return 0, fmt.Errorf("utc-offset has invalid length: %q", s)
	}
	hours, err := strconv.Atoi(s[:2])
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(s[2:4])
	if err != nil {
		return 0, err
	}
	seconds := 0
	if len(s) == 6 {
		seconds, err = strconv.Atoi(s[4:6])
		if err != nil {
			return 0, err
		}
	}
	return sign * (hours*3600 + minutes*60 + seconds), nil
}

// resolveComponentTZIDs rewrites TZID parameters on datetime properties
// (DTSTART, DTEND, DUE) so that non-IANA TZIDs are replaced with their
// IANA equivalents. This allows go-ical's DateTime() to resolve them via
// time.LoadLocation.
func resolveComponentTZIDs(comp *goical.Component, tzMap map[string]*time.Location) {
	resolvePropTZID := func(p *goical.Prop) {
		if p == nil {
			return
		}
		tzid := p.Params.Get(goical.ParamTimezoneID)
		if tzid == "" {
			return
		}
		// Already valid IANA?
		if _, err := time.LoadLocation(tzid); err == nil {
			return
		}
		// Windows alias?
		if iana, ok := windowsToIANA[tzid]; ok {
			p.Params.Set(goical.ParamTimezoneID, iana)
			return
		}
		// VTIMEZONE-derived fixed zone — parse using the offset and rewrite the
		// instant as UTC (go-ical's DateTime() can't LoadLocation a private
		// TZID, so we resolve the offset here). The TZID parameter is kept so
		// the original zone identity survives import: go-ical parses a value
		// with a trailing "Z" as UTC and ignores the TZID, while the per-domain
		// builders still read the TZID label into the stored Timezone. Dropping
		// the param here would silently collapse the event to a plain UTC event
		// (issue #131). EXDATE/RDATE may carry a comma-separated list of
		// datetimes in a single value, so convert each element. Only rewrite if
		// every element parses, otherwise leave the value untouched.
		if loc, ok := tzMap[tzid]; ok {
			parts := strings.Split(p.Value, ",")
			converted := make([]string, len(parts))
			allOK := true
			for i, part := range parts {
				t, err := time.ParseInLocation("20060102T150405", strings.TrimSpace(part), loc)
				if err != nil {
					allOK = false
					break
				}
				converted[i] = t.UTC().Format("20060102T150405Z")
			}
			if allOK {
				p.Value = strings.Join(converted, ",")
			}
		}
	}

	for _, propName := range []string{
		goical.PropDateTimeStart,
		goical.PropDateTimeEnd,
		goical.PropDue,
		goical.PropExceptionDates,
		goical.PropRecurrenceDates,
		goical.PropRecurrenceID,
	} {
		key := strings.ToUpper(propName)
		props := comp.Props[key]
		for i := range props {
			resolvePropTZID(&props[i])
		}
		comp.Props[key] = props
	}

	// Also resolve TRIGGER TZIDs in VALARM sub-components.
	for _, sub := range comp.Children {
		if sub.Name == goical.CompAlarm {
			resolvePropTZID(sub.Props.Get(goical.PropTrigger))
		}
	}
}

// windowsToIANA maps common Windows timezone names to IANA identifiers.
// Source: Unicode CLDR windowsZones.xml (subset covering major zones).
var windowsToIANA = map[string]string{
	"AUS Central Standard Time":       "Australia/Darwin",
	"AUS Eastern Standard Time":       "Australia/Sydney",
	"Afghanistan Standard Time":       "Asia/Kabul",
	"Alaskan Standard Time":           "America/Anchorage",
	"Arab Standard Time":              "Asia/Riyadh",
	"Arabian Standard Time":           "Asia/Dubai",
	"Arabic Standard Time":            "Asia/Baghdad",
	"Argentina Standard Time":         "America/Buenos_Aires",
	"Atlantic Standard Time":          "America/Halifax",
	"Azerbaijan Standard Time":        "Asia/Baku",
	"Azores Standard Time":            "Atlantic/Azores",
	"Canada Central Standard Time":    "America/Regina",
	"Cape Verde Standard Time":        "Atlantic/Cape_Verde",
	"Central America Standard Time":   "America/Guatemala",
	"Central Asia Standard Time":      "Asia/Almaty",
	"Central Brazilian Standard Time": "America/Cuiaba",
	"Central Europe Standard Time":    "Europe/Budapest",
	"Central European Standard Time":  "Europe/Warsaw",
	"Central Pacific Standard Time":   "Pacific/Guadalcanal",
	"Central Standard Time":           "America/Chicago",
	"Central Standard Time (Mexico)":  "America/Mexico_City",
	"China Standard Time":             "Asia/Shanghai",
	"E. Africa Standard Time":         "Africa/Nairobi",
	"E. Australia Standard Time":      "Australia/Brisbane",
	"E. Europe Standard Time":         "Europe/Chisinau",
	"E. South America Standard Time":  "America/Sao_Paulo",
	"Eastern Standard Time":           "America/New_York",
	"Eastern Standard Time (Mexico)":  "America/Cancun",
	"Egypt Standard Time":             "Africa/Cairo",
	"FLE Standard Time":               "Europe/Kiev",
	"GMT Standard Time":               "Europe/London",
	"GTB Standard Time":               "Europe/Bucharest",
	"Georgian Standard Time":          "Asia/Tbilisi",
	"Greenwich Standard Time":         "Atlantic/Reykjavik",
	"Hawaiian Standard Time":          "Pacific/Honolulu",
	"India Standard Time":             "Asia/Calcutta",
	"Iran Standard Time":              "Asia/Tehran",
	"Israel Standard Time":            "Asia/Jerusalem",
	"Jordan Standard Time":            "Asia/Amman",
	"Korea Standard Time":             "Asia/Seoul",
	"Mauritius Standard Time":         "Indian/Mauritius",
	"Middle East Standard Time":       "Asia/Beirut",
	"Mountain Standard Time":          "America/Denver",
	"Mountain Standard Time (Mexico)": "America/Chihuahua",
	"Myanmar Standard Time":           "Asia/Rangoon",
	"N. Central Asia Standard Time":   "Asia/Novosibirsk",
	"Nepal Standard Time":             "Asia/Katmandu",
	"New Zealand Standard Time":       "Pacific/Auckland",
	"Newfoundland Standard Time":      "America/St_Johns",
	"North Asia East Standard Time":   "Asia/Irkutsk",
	"North Asia Standard Time":        "Asia/Krasnoyarsk",
	"Pacific SA Standard Time":        "America/Santiago",
	"Pacific Standard Time":           "America/Los_Angeles",
	"Pacific Standard Time (Mexico)":  "America/Tijuana",
	"Pakistan Standard Time":          "Asia/Karachi",
	"Romance Standard Time":           "Europe/Paris",
	"Russian Standard Time":           "Europe/Moscow",
	"SA Eastern Standard Time":        "America/Cayenne",
	"SA Pacific Standard Time":        "America/Bogota",
	"SA Western Standard Time":        "America/La_Paz",
	"SE Asia Standard Time":           "Asia/Bangkok",
	"Samoa Standard Time":             "Pacific/Apia",
	"Singapore Standard Time":         "Asia/Singapore",
	"South Africa Standard Time":      "Africa/Johannesburg",
	"Sri Lanka Standard Time":         "Asia/Colombo",
	"Taipei Standard Time":            "Asia/Taipei",
	"Tasmania Standard Time":          "Australia/Hobart",
	"Tokyo Standard Time":             "Asia/Tokyo",
	"Tonga Standard Time":             "Pacific/Tongatapu",
	"Turkey Standard Time":            "Europe/Istanbul",
	"US Eastern Standard Time":        "America/Indianapolis",
	"US Mountain Standard Time":       "America/Phoenix",
	"UTC":                             "Etc/UTC",
	"Venezuela Standard Time":         "America/Caracas",
	"W. Australia Standard Time":      "Australia/Perth",
	"W. Central Africa Standard Time": "Africa/Lagos",
	"W. Europe Standard Time":         "Europe/Berlin",
	"West Asia Standard Time":         "Asia/Tashkent",
	"West Pacific Standard Time":      "Pacific/Port_Moresby",
	"Yakutsk Standard Time":           "Asia/Yakutsk",
}
