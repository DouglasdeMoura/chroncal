package model

type Relation struct {
	ID int64
	// RelType holds the RFC 5545 RELTYPE parameter. The standard names
	// PARENT, CHILD, and SIBLING, but RELTYPE is an extensible token. A
	// server can send another token, for example X-APPLE-SOMETHING. The
	// database keeps the token as it arrives, so an export gives it back
	// unchanged. Only an empty token is invalid.
	RelType string
	RelUID  string // UID of related component
}
