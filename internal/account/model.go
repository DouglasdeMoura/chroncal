package account

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/publicsuffix"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
)

const legacyHiddenPrefix = "__calendar_"

// Account is a configured CalDAV identity shared by one or more calendars.
type Account struct {
	ID           int64
	Name         string
	DisplayName  string
	ServerURL    string
	AuthType     string
	DisplayOrder int64
	Username     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (a Account) CredentialFingerprint() string {
	return auth.AccountFingerprint(a.ServerURL, a.AuthType, a.Username)
}

// SuggestedName turns a credential identifier into a short account
// description suitable for the sidebar. Common consumer domains use their
// provider name; other email identities use the registrable organization
// domain. The suggestion remains editable because domain labels are not brands.
func SuggestedName(username string) string {
	username = strings.TrimSpace(username)
	at := strings.LastIndexByte(username, '@')
	if at < 0 || at == len(username)-1 {
		return username
	}
	domain := strings.ToLower(strings.TrimSuffix(username[at+1:], "."))
	switch domain {
	case "gmail.com", "googlemail.com":
		return "Google"
	case "icloud.com", "me.com", "mac.com":
		return "iCloud"
	case "yahoo.com":
		return "Yahoo"
	}
	registrable, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		registrable = domain
	}
	label, _, _ := strings.Cut(registrable, ".")
	if label == "" {
		return username
	}
	first, size := utf8.DecodeRuneInString(label)
	if first == utf8.RuneError && size == 1 {
		return label
	}
	return string(unicode.ToUpper(first)) + label[size:]
}

// UserFacingName hides the old per-calendar implementation name when loading
// accounts created before account management became user-visible.
func UserFacingName(name, username string, id int64) string {
	name = strings.TrimSpace(name)
	username = strings.TrimSpace(username)
	generatedFromCredential := strings.HasPrefix(name, legacyHiddenPrefix) ||
		(username != "" && strings.EqualFold(name, username))
	if !generatedFromCredential {
		return name
	}
	if username != "" {
		return SuggestedName(username)
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

// RemoveParams configures destructive local removal of an account and every
// calendar attached to it. NewDefaultID is required when the removed account
// owns the current default calendar.
type RemoveParams struct {
	NewDefaultID int64
}

// RemoveResult reports the local calendar rows removed with an account.
type RemoveResult struct {
	RemovedIDs []int64
}

// CreateParams contains non-secret account connection settings.
type CreateParams struct {
	Name          string
	ServerURL     string
	AuthType      string
	Username      string
	AllowInsecure bool
}
