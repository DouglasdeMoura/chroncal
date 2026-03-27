package event

import (
	"strings"
	"time"
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
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// Transient fields — populated for import/export, not stored in events table
	Alarms    []Alarm
	Attendees []Attendee
}

type Alarm struct {
	ID           int64
	EventID      int64
	Action       string // DISPLAY, EMAIL, AUDIO
	TriggerValue string // e.g. "-PT15M" or absolute RFC 3339
	Description  string
}

type Attendee struct {
	ID         int64
	EventID    int64
	Email      string
	Name       string
	RSVPStatus string // NEEDS-ACTION, ACCEPTED, DECLINED, TENTATIVE
	Role       string // REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR
	Organizer  bool
}

func (e Event) Duration() time.Duration {
	return e.EndTime.Sub(e.StartTime)
}

func (e Event) IsRecurrenceOverride() bool {
	return e.RecurrenceID != ""
}

func (e Event) ParseExDates() []time.Time {
	return parseTimeList(e.ExDates)
}

func (e Event) ParseRDates() []time.Time {
	return parseTimeList(e.RDates)
}

func (e Event) ParseCategories() []string {
	if e.Categories == "" {
		return nil
	}
	parts := strings.Split(e.Categories, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parseTimeList(s string) []time.Time {
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
