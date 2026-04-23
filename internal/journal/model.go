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

	Attendees   []model.Attendee
	Attachments []model.Attachment
	Comments    []string
	Contacts    []string
	Relations   []model.Relation
	XProperties []model.XProperty
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
