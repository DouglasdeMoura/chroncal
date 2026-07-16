package account

import (
	"fmt"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
)

const legacyHiddenPrefix = "__calendar_"

// Account is a configured CalDAV identity shared by one or more calendars.
type Account struct {
	ID          int64
	Name        string
	DisplayName string
	ServerURL   string
	AuthType    string
	Username    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (a Account) CredentialFingerprint() string {
	return auth.AccountFingerprint(a.ServerURL, a.AuthType, a.Username)
}

// UserFacingName hides the old per-calendar implementation name when loading
// accounts created before account management became user-visible.
func UserFacingName(name, username string, id int64) string {
	if !strings.HasPrefix(name, legacyHiddenPrefix) {
		return name
	}
	if username = strings.TrimSpace(username); username != "" {
		return username
	}
	return fmt.Sprintf("Remote account %d", id)
}

// DiscoveredCalendar joins remote collection metadata with its local import
// state so CLI and TUI callers can render one selection model.
type DiscoveredCalendar struct {
	caldav.RemoteCalendar
	CalendarID int64
	Imported   bool
	Importable bool
	Missing    bool
}

// Discovery is a complete collection inventory for one account.
type Discovery struct {
	Account   Account
	Calendars []DiscoveredCalendar
}

// ImportResult separates newly created calendars from already-linked choices.
type ImportResult struct {
	CreatedIDs  []int64
	ExistingIDs []int64
}

// SelectionParams describes the desired final set of local calendars for one
// discovered account. A replacement default may identify an existing local
// calendar by ID or a newly selected collection by path.
type SelectionParams struct {
	SelectedPaths  []string
	NewDefaultID   int64
	NewDefaultPath string
}

// SelectionResult reports the local rows changed by ReconcileSelection.
type SelectionResult struct {
	CreatedIDs     []int64
	RemovedIDs     []int64
	AccountRemoved bool
}

// CreateParams contains non-secret account connection settings.
type CreateParams struct {
	Name          string
	ServerURL     string
	AuthType      string
	Username      string
	AllowInsecure bool
}
