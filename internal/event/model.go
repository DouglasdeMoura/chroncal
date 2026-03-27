package event

import "time"

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
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (e Event) Duration() time.Duration {
	return e.EndTime.Sub(e.StartTime)
}
