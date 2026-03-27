package model

type Attendee struct {
	ID         int64
	EventID    int64
	Email      string
	Name       string
	RSVPStatus string // NEEDS-ACTION, ACCEPTED, DECLINED, TENTATIVE
	Role       string // REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR
	Organizer  bool
}
