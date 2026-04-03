package model

// XProperty represents an iCal extension property (X-* or unhandled IANA
// property) stored for round-trip fidelity. The Params field is a JSON
// object mapping parameter names to arrays of values, e.g.:
//
//	{"LANGUAGE": ["en"], "X-CUSTOM": ["a", "b"]}
type XProperty struct {
	ID        int64
	OwnerType string // "event", "todo", "journal"
	OwnerID   int64
	Name      string
	Value     string
	Params    string // JSON object
}
