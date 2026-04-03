package event

import (
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

type Event struct {
	ID             int64
	UID            string
	CalendarID     int64
	Title          string
	Description    string
	Location       string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
	Timezone       string
	Status         string // TENTATIVE, CONFIRMED, CANCELLED
	Transp         string // OPAQUE, TRANSPARENT
	Sequence       int64
	Priority       int64 // 0-9
	Class          string // PUBLIC, PRIVATE, CONFIDENTIAL
	URL            string
	Categories     string // comma-separated
	ExDates        string // comma-separated RFC 3339
	RDates         string // comma-separated RFC 3339
	RecurrenceID   string // RFC 3339 of overridden instance
	Geo            string // "lat;lon" (RFC 5545 GEO format)
	DurationValue  string // RFC 5545 DURATION string (e.g. "PT1H"); empty = use DTEND
	DtStamp        string // RFC 5545 DTSTAMP; empty = use UpdatedAt
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// Transient fields — populated for import/export, not stored in events table
	Alarms      []model.Alarm
	Attendees   []model.Attendee
	Attachments []model.Attachment
	Comments    []string
	Contacts    []string
	Resources   []string
	Relations   []model.Relation
}

// Span returns the computed time.Duration between StartTime and EndTime.
func (e Event) Span() time.Duration {
	return e.EndTime.Sub(e.StartTime)
}

func (e Event) IsRecurrenceOverride() bool {
	return e.RecurrenceID != ""
}

func (e Event) ParseExDates() []time.Time {
	return ParseTimeList(e.ExDates)
}

func (e Event) ParseRDates() []time.Time {
	return ParseTimeList(e.RDates)
}

func (e Event) ParseCategories() []string {
	return ParseCategoryList(e.Categories)
}

// ParseTimeList delegates to timeutil.ParseTimeList.
func ParseTimeList(s string) []time.Time { return timeutil.ParseTimeList(s) }

// SerializeTimeList delegates to timeutil.SerializeTimeList.
func SerializeTimeList(times []time.Time) string { return timeutil.SerializeTimeList(times) }

// ParseCategoryList delegates to timeutil.ParseCategoryList.
func ParseCategoryList(s string) []string { return timeutil.ParseCategoryList(s) }
