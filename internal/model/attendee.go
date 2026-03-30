package model

type Attendee struct {
	ID            int64
	EventID       int64
	Email         string
	Name          string // CN
	RSVPStatus    string // PARTSTAT: NEEDS-ACTION, ACCEPTED, DECLINED, TENTATIVE
	Role          string // ROLE: REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR
	Organizer     bool
	CUType        string // CUTYPE: INDIVIDUAL, GROUP, RESOURCE, ROOM, UNKNOWN
	RSVPRequested bool   // RSVP: boolean flag
	SentBy        string // SENT-BY: mailto URI of acting user
	DelegatedTo   string // DELEGATED-TO: comma-separated mailto URIs
	DelegatedFrom string // DELEGATED-FROM: comma-separated mailto URIs
	Member        string // MEMBER: comma-separated group membership mailto URIs
	Dir           string // DIR: directory entry URI
	Language      string // LANGUAGE: language tag (e.g. en-US)
}
