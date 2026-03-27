package calendar

import "time"

type Calendar struct {
	ID          int64
	Name        string
	Color       string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
