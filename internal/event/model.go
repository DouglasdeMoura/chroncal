package event

import (
	"strings"
	"time"

	"github.com/douglasdemoura/tcal/internal/model"
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
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// Transient fields — populated for import/export, not stored in events table
	Alarms      []model.Alarm
	Attendees   []model.Attendee
	Attachments []model.Attachment
	Comments    []string
	Relations   []model.Relation
}

func (e Event) Duration() time.Duration {
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

func ParseTimeList(s string) []time.Time {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]time.Time, 0, len(parts))
	for _, p := range parts {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(p)); err == nil {
			out = append(out, t)
		}
	}
	return out
}

func ParseCategoryList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
