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

	OwnerEmail string // Email of the calendar owner (for RSVP matching)

	DisplayOrder int64 // Sidebar sort position; lower sorts first

	// Sync fields — populated when calendar is linked to a remote account
	AccountID           int64  // 0 = local-only calendar
	RemoteURL           string // CalDAV calendar URL (href)
	CTag                string // CalDAV getctag for change detection
	SyncToken           string // CalDAV sync-token (preferred over ctag)
	LastSyncAt          string // RFC 3339 timestamp of last clean sync
	LastSyncAttemptedAt string // RFC 3339 timestamp of last sync attempt
	LastSyncError       string // Concise summary of the last sync failure
	RemoteColor         string // Last known remote calendar-color value
	ColorDirty          bool   // Local color changed and needs remote sync
	RemoteName          string // last display name returned by collection discovery
	RemoteAccess        string // "unknown", "read", "write", or "owner"
	RemoteComponents    string // comma-separated VEVENT/VTODO/VJOURNAL capabilities
	RemoteMissing       bool   // collection absent from the last complete discovery

	IsDefault bool // True when this is the default calendar
}
