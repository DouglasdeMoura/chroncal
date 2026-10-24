package journal

import (
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

type Journal struct {
	ID             int64
	UID            string
	CalendarID     int64
	Summary        string
	Description    string
	StartDate      string // YYYY-MM-DD (date-only) or RFC 3339 or empty
	Status         string // DRAFT, FINAL, CANCELLED
	Class          string // PUBLIC, PRIVATE, CONFIDENTIAL
	URL            string
	Categories     string // comma-separated
	RecurrenceRule string
	Timezone       string
	Sequence       int64
	ExDates        string
	RDates         string
	RecurrenceID   string
	DtStamp        string // RFC 5545 DTSTAMP; empty = use UpdatedAt
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time // nil = not soft-deleted; set = soft-deleted at this time
	// RecurrenceTime is the RECURRENCE-ID of the occurrence that a list
	// expansion gives: a generated occurrence or an override. It is zero
	// for another row. The database does not store it.
	RecurrenceTime time.Time

	// Descriptions carries every DESCRIPTION property for iCal round-trip
	// fidelity. RFC 5545 permits a VJOURNAL to hold multiple DESCRIPTIONs;
	// the Description field above is the joined, DB-backed value used for
	// display and search, while this transient slice preserves the original
	// per-property split on export (mirroring Comments/Contacts).
	Descriptions []string

	Attendees   []model.Attendee
	Attachments []model.Attachment
	Comments    []string
	Contacts    []string
	Relations   []model.Relation
	XProperties []model.XProperty
}

// IsDateOnly reports whether the DTSTART of the journal is a date-only
// value. A date-only value is a floating date (see timeutil.FloatingLocal).
func (j Journal) IsDateOnly() bool {
	return timeutil.IsDateOnly(j.StartDate)
}

// LocalStart returns the DTSTART of the journal as a local moment, or the
// zero time. A date-only DTSTART starts at local midnight of its date
// (timeutil.FloatingLocal). A sort of journals uses it.
func (j Journal) LocalStart() time.Time {
	return timeutil.LocalMoment(j.StartDate)
}

func (j Journal) ParseStartDate() time.Time {
	return timeutil.ParseDate(j.StartDate)
}

func (j Journal) ParseExDates() []time.Time {
	return timeutil.ParseTimeList(j.ExDates)
}

func (j Journal) ParseRDates() []time.Time {
	return timeutil.ParseTimeList(j.RDates)
}

func (j Journal) ParseCategories() []string {
	return timeutil.ParseCategoryList(j.Categories)
}
