package todo

import (
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

type Todo struct {
	ID              int64
	UID             string
	CalendarID      int64
	Summary         string
	Description     string
	Location        string
	DueDate         string // YYYY-MM-DD (date-only) or RFC 3339 or empty
	StartDate       string // YYYY-MM-DD (date-only) or RFC 3339 or empty
	Duration        string // RFC 5545 duration or empty
	CompletedAt     string // RFC 3339 or empty
	PercentComplete int64
	Status          string // NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED
	Priority        int64
	Class           string // PUBLIC, PRIVATE, CONFIDENTIAL
	URL             string
	Categories      string // comma-separated
	RecurrenceRule  string
	Timezone        string
	Sequence        int64
	ExDates         string
	RDates          string
	RecurrenceID    string
	Geo             string // "lat;lon" (RFC 5545 GEO format)
	DtStamp         string // RFC 5545 DTSTAMP; empty = use UpdatedAt
	CreatedAt       time.Time
	UpdatedAt       time.Time

	Alarms      []model.Alarm
	Attendees   []model.Attendee
	Attachments []model.Attachment
	Comments    []string
	Contacts    []string
	Resources   []string
	Relations   []model.Relation
}

func (t Todo) IsCompleted() bool {
	return t.Status == "COMPLETED"
}

func (t Todo) IsOverdue() bool {
	if t.IsCompleted() {
		return false
	}
	due := t.ParseDueDate()
	if due.IsZero() {
		return false
	}
	now := time.Now()
	// Date-only: overdue after end of that day in local time
	if timeutil.IsDateOnly(t.DueDate) {
		endOfDay := time.Date(due.Year(), due.Month(), due.Day(), 23, 59, 59, 0, time.Local)
		return now.After(endOfDay)
	}
	return now.After(due)
}

func (t Todo) ParseDueDate() time.Time {
	return timeutil.ParseDate(t.DueDate)
}

func (t Todo) ParseStartDate() time.Time {
	return timeutil.ParseDate(t.StartDate)
}

func (t Todo) ParseCompletedAt() time.Time {
	return timeutil.ParseDateTime(t.CompletedAt)
}

func (t Todo) ParseExDates() []time.Time {
	return event.ParseTimeList(t.ExDates)
}

func (t Todo) ParseRDates() []time.Time {
	return event.ParseTimeList(t.RDates)
}

func (t Todo) ParseCategories() []string {
	return event.ParseCategoryList(t.Categories)
}
