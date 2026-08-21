package model

// XPropOriginalDTEND names the x-property that preserves a server DTEND
// which local parsing could not read. The CalDAV pull path stores the raw
// property under this name. Export emits the stored string as DTEND while
// no DURATION is set. A local edit that changes the span clears the slot.
// See internal/ical and docs/adr/0002 (issues #567, #649).
const XPropOriginalDTEND = "X-CHRONCAL-ORIGINAL-DTEND"

// XProperty represents an iCal extension property (X-* or unhandled IANA
// property) stored for round-trip fidelity. The Params field is a JSON
// object mapping parameter names to arrays of values, e.g.:
//
//	{"LANGUAGE": ["en"], "X-CUSTOM": ["a", "b"]}
type XProperty struct {
	ID        int64
	OwnerType string // "event", "todo", "journal", "event_alarm", "todo_alarm"
	OwnerID   int64
	Name      string
	Value     string
	Params    string // JSON object
}

// XPropsContentEqual reports whether two X-property sets carry the same
// content (name, value, params) in the same order. IDs and owners are ignored.
func XPropsContentEqual(a, b []XProperty) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Value != b[i].Value {
			return false
		}
		pa, pb := a[i].Params, b[i].Params
		if pa == "" {
			pa = "{}"
		}
		if pb == "" {
			pb = "{}"
		}
		if pa != pb {
			return false
		}
	}
	return true
}
