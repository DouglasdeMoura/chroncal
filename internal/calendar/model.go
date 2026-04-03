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

	// Sync fields — populated when calendar is linked to a remote account
	AccountID int64  // 0 = local-only calendar
	RemoteURL string // CalDAV calendar URL (href)
	CTag      string // CalDAV getctag for change detection
	SyncToken string // CalDAV sync-token (preferred over ctag)
}
