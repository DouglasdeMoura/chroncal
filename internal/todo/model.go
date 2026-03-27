package todo

import (
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
)

type Todo struct {
	ID              int64
	UID             string
	CalendarID      int64
	Summary         string
	Description     string
	Location        string
	DueDate         string // RFC 3339 or empty
	StartDate       string // RFC 3339 or empty
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
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// Transient fields
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
	if t.DueDate == "" || t.IsCompleted() {
		return false
	}
	due := t.ParseDueDate()
	return !due.IsZero() && time.Now().After(due)
}

func (t Todo) ParseDueDate() time.Time {
	if t.DueDate == "" {
		return time.Time{}
	}
	p, _ := time.Parse(time.RFC3339, t.DueDate)
	return p
}

func (t Todo) ParseStartDate() time.Time {
	if t.StartDate == "" {
		return time.Time{}
	}
	p, _ := time.Parse(time.RFC3339, t.StartDate)
	return p
}

func (t Todo) ParseCompletedAt() time.Time {
	if t.CompletedAt == "" {
		return time.Time{}
	}
	p, _ := time.Parse(time.RFC3339, t.CompletedAt)
	return p
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
