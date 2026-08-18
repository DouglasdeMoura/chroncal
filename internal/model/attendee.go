package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

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

// AttendeeKind selects the PARTSTAT set one component accepts. The event
// table and the task tables carry different CHECK constraints, so a
// caller must name the component it writes.
type AttendeeKind int

const (
	// EventAttendee names the PARTSTAT set of event_attendees.
	EventAttendee AttendeeKind = iota
	// TaskAttendee names the wider PARTSTAT set of todo_attendees and
	// journal_attendees. Those two tables also accept COMPLETED and
	// IN-PROCESS (RFC 5545 §3.2.12).
	TaskAttendee
)

// The default values the attendee tables carry on the two NOT NULL
// columns. PrepareAttendeesForWrite fills an empty field with them.
const (
	DefaultRSVPStatus   = "NEEDS-ACTION"
	DefaultAttendeeRole = "REQ-PARTICIPANT"
	// UnknownCUType is the value RFC 5545 §3.2.3 reserves for a
	// calendar user type the reader does not know. The import parser
	// maps an x-name or iana-token CUTYPE to it.
	UnknownCUType = "UNKNOWN"
)

// The accepted attendee values, in a fixed order. Each slice mirrors one
// CHECK constraint in db/migrations/003, 006, and 013. Keep every slice
// and its constraints in lockstep. A value that passes here but fails a
// constraint rolls back the whole resource transaction during sync
// (issue #575). The match is case-sensitive, the same as the
// constraints. The slices are unexported so no other package can change
// a set at run time.
var (
	eventRSVPStatuses = []string{"NEEDS-ACTION", "ACCEPTED", "DECLINED", "TENTATIVE", "DELEGATED"}
	taskRSVPStatuses  = []string{"NEEDS-ACTION", "ACCEPTED", "DECLINED", "TENTATIVE", "DELEGATED", "COMPLETED", "IN-PROCESS"}
	attendeeRoles     = []string{"CHAIR", "REQ-PARTICIPANT", "OPT-PARTICIPANT", "NON-PARTICIPANT"}
	attendeeCUTypes   = []string{"INDIVIDUAL", "GROUP", "RESOURCE", "ROOM", "UNKNOWN"}
)

// The joined lists render once at package load, so every error message
// stays in lockstep with the slices.
var (
	eventRSVPStatusesList = strings.Join(eventRSVPStatuses, ", ")
	taskRSVPStatusesList  = strings.Join(taskRSVPStatuses, ", ")
	attendeeRolesList     = strings.Join(attendeeRoles, ", ")
	attendeeCUTypesList   = strings.Join(attendeeCUTypes, ", ")
)

// RSVPStatuses returns the accepted PARTSTAT values of one component as
// a fresh slice. A caller can keep or change the returned slice. The set
// does not change.
func RSVPStatuses(kind AttendeeKind) []string {
	return slices.Clone(rsvpStatusesFor(kind))
}

// AttendeeRoles returns the accepted ROLE values as a fresh slice, like
// RSVPStatuses.
func AttendeeRoles() []string {
	return slices.Clone(attendeeRoles)
}

// AttendeeCUTypes returns the accepted CUTYPE values as a fresh slice,
// like RSVPStatuses.
func AttendeeCUTypes() []string {
	return slices.Clone(attendeeCUTypes)
}

// RSVPStatusesList returns the accepted PARTSTAT values of one component
// as one joined string, for an error message or help text.
func RSVPStatusesList(kind AttendeeKind) string {
	if kind == TaskAttendee {
		return taskRSVPStatusesList
	}
	return eventRSVPStatusesList
}

// AttendeeRolesList returns the accepted ROLE values as one joined
// string, like RSVPStatusesList.
func AttendeeRolesList() string {
	return attendeeRolesList
}

// AttendeeCUTypesList returns the accepted CUTYPE values as one joined
// string, like RSVPStatusesList.
func AttendeeCUTypesList() string {
	return attendeeCUTypesList
}

func rsvpStatusesFor(kind AttendeeKind) []string {
	if kind == TaskAttendee {
		return taskRSVPStatuses
	}
	return eventRSVPStatuses
}

// ValidRSVPStatus returns true if status is a PARTSTAT value the table of
// that component accepts.
func ValidRSVPStatus(kind AttendeeKind, status string) bool {
	return slices.Contains(rsvpStatusesFor(kind), status)
}

// ValidAttendeeRole returns true if role is a ROLE value the attendee
// tables accept.
func ValidAttendeeRole(role string) bool {
	return slices.Contains(attendeeRoles, role)
}

// ValidAttendeeCUType returns true if cutype is a CUTYPE value the
// attendee tables accept. The column holds NULL, so an empty value
// passes.
func ValidAttendeeCUType(cutype string) bool {
	return cutype == "" || slices.Contains(attendeeCUTypes, cutype)
}

// ErrInvalidAttendee marks an attendee field value the attendee tables
// reject. PrepareAttendeesForWrite wraps it with the field and the value.
var ErrInvalidAttendee = errors.New("invalid attendee")

// PrepareAttendeesForWrite returns a prepared copy of attendees. For each
// element it fills an empty RSVPStatus with DefaultRSVPStatus and an
// empty Role with DefaultAttendeeRole. It then validates the three
// fields with a CHECK constraint against the sets the tables accept. For
// a bad value it returns an error that wraps ErrInvalidAttendee and
// names the index, the field, and the value. The caller's slice does not
// change.
//
// The event, todo, and journal services call this function before every
// attendee write. The iCal import parser stays the first guard: it
// clamps an out-of-set value and warns. The typed error names the cause
// instead of a raw CHECK failure (issues #575, #587).
func PrepareAttendeesForWrite(kind AttendeeKind, attendees []Attendee) ([]Attendee, error) {
	prepared := slices.Clone(attendees)
	for i := range prepared {
		if err := prepareAttendeeForWrite(kind, &prepared[i]); err != nil {
			return nil, fmt.Errorf("attendee %d: %w", i+1, err)
		}
	}
	return prepared, nil
}

// prepareAttendeeForWrite fills the defaults and validates one attendee.
// The CUTYPE column holds NULL, so an empty value stays empty.
func prepareAttendeeForWrite(kind AttendeeKind, a *Attendee) error {
	if a.RSVPStatus == "" {
		a.RSVPStatus = DefaultRSVPStatus
	}
	if a.Role == "" {
		a.Role = DefaultAttendeeRole
	}
	if !ValidRSVPStatus(kind, a.RSVPStatus) {
		return fmt.Errorf("%w: partstat %q is not one of %s", ErrInvalidAttendee, a.RSVPStatus, RSVPStatusesList(kind))
	}
	if !ValidAttendeeRole(a.Role) {
		return fmt.Errorf("%w: role %q is not one of %s", ErrInvalidAttendee, a.Role, attendeeRolesList)
	}
	if !ValidAttendeeCUType(a.CUType) {
		return fmt.Errorf("%w: cutype %q is not one of %s", ErrInvalidAttendee, a.CUType, attendeeCUTypesList)
	}
	return nil
}
