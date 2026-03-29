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
	if due.IsZero() {
		return false
	}
	// Date-only: overdue after end of that day in local time
	if isDateOnly(t.DueDate) {
		endOfDay := time.Date(due.Year(), due.Month(), due.Day(), 23, 59, 59, 0, time.Local)
		return time.Now().After(endOfDay)
	}
	return time.Now().After(due)
}

func (t Todo) ParseDueDate() time.Time {
	if t.DueDate == "" {
		return time.Time{}
	}
	if p, err := time.Parse("2006-01-02", t.DueDate); err == nil {
		return p
	}
	p, _ := time.Parse(time.RFC3339, t.DueDate)
	return p
}

func (t Todo) ParseStartDate() time.Time {
	if t.StartDate == "" {
		return time.Time{}
	}
	if p, err := time.Parse("2006-01-02", t.StartDate); err == nil {
		return p
	}
	p, _ := time.Parse(time.RFC3339, t.StartDate)
	return p
}

func isDateOnly(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
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
