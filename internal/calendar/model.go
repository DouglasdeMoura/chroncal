package calendar

import "time"

// Calendar represents a user calendar.
type Calendar struct {
	ID          int64
	Name        string
	Color       string // Hex color code (e.g., "#7C3AED")
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
