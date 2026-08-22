package tui

import (
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/viewport"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
)

// CalendarDialogParams seeds the calendar dialog. All fields are optional.
// ID == 0 means "create a new calendar". RemoteLinked reflects whether
// the calendar is currently connected to a remote CalDAV account.
type CalendarDialogParams struct {
	ID           int64
	AccountID    int64
	AccountName  string
	Name         string
	Color        string // hex like "#a6e3a1"
	Description  string
	OwnerEmail   string
	RemoteLinked bool

	// LastSyncAt and LastSyncError are compact, display-only account context
	// for linked calendars. Account maintenance lives in Account Settings.
	LastSyncAt    string // RFC 3339, empty when never synced cleanly
	LastSyncError string

	// IsDefault marks the calendar being edited as the current default. It
	// drives the dialog's "Default calendar" badge and hides the redundant
	// Set-as-Default action. Ignored in create mode.
	IsDefault bool

	// OfferDefault enables the "Set as default after saving" checkbox in
	// create mode. Callers set it when at least one calendar already
	// exists. The service auto-promotes the first calendar. The checkbox
	// would be meaningless and noisy in that case.
	OfferDefault bool

	// Hidden is the calendar's current sidebar visibility. The edit dialog
	// mirrors it into a Display Calendar checkbox whose toggle emits
	// CalendarVisibilityToggledMsg with the desired state immediately;
	// metadata Save/Cancel never auto-persists visibility.
	Hidden bool

	// ManagerEmbedded marks this detail as hosted inside the CalendarManager
	// rather than opened directly by the legacy app. It gates manager-only
	// affordances whose host-side handler does not exist yet (currently
	// Export), so a legacy-wired dialog never exposes a no-op action. The
	// manager sets it via OpenCalendar; legacy callers leave it false.
	ManagerEmbedded bool
}

// CalendarSavedMsg is emitted when the user saves the dialog. ID == 0 means
// "create a new calendar"; otherwise it's an update.
type CalendarSavedMsg struct {
	ID          int64
	Name        string
	Color       string
	Description string
	OwnerEmail  string

	// MakeDefault, when true on a create (ID == 0), instructs the parent to
	// promote the just-created calendar to default after the row is saved.
	// Ignored on edit — defaultness moves via SetDefault, not Save.
	MakeDefault bool
}

// CalendarDiscoveryRequestedMsg starts discovery from the Add Account flow.
// Remote collection metadata supplies the local calendars after sign-in.
type CalendarDiscoveryRequestedMsg struct {
	ServerURL         string
	Username          string
	AuthType          string
	Secret            string
	OAuthClientID     string
	OAuthClientSecret string
	AllowInsecure     bool
}

// CalendarDeleteRequestedMsg is emitted when the user presses Delete in the
// dialog. The parent is responsible for the confirm dialog.
type CalendarDeleteRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarExportRequestedMsg is a neutral request to export the calendar. The
// parent owns the file I/O. This message only identifies the target by its
// immutable ID so the host can resolve fresh data at export time.
type CalendarExportRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarMoveToAccountRequestedMsg starts migration of a local calendar into
// a collection that already exists, owned by a configured account.
type CalendarMoveToAccountRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarSetDefaultRequestedMsg asks the app to promote a calendar to default.
type CalendarSetDefaultRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarKeepLocalRequestedMsg asks the app to unlink an account calendar
// while every downloaded event stays as a local calendar. That is the
// keep-local counterpart to a remove in Manage Calendars. The parent owns
// the confirm flow and the Disconnect call.
type CalendarKeepLocalRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarTestRequestedMsg is emitted when the user presses Test. The parent
// runs a CalDAV authenticated ping and replies with CalendarTestResultMsg.
type CalendarTestRequestedMsg struct {
	URL           string
	Username      string
	AuthType      string
	Password      string
	AllowInsecure bool
}

// CalendarTestResultMsg is the outcome of a CalendarTestRequestedMsg.
type CalendarTestResultMsg struct {
	OK      bool
	Message string
}

// testConnectionPressedMsg is an internal sentinel emitted by the Test
// button. The dialog can then read the current field values before it asks
// the parent to perform the actual connection check.
type testConnectionPressedMsg struct{}

// calendarSavePromotePressedMsg is an internal sentinel emitted by the
// "Save and Set as Default" button. The dialog catches it, sets the
// makeDefault flag observed by OnSubmit, and triggers the normal Save
// pipeline so the same validation runs.
type calendarSavePromotePressedMsg struct{}

// CalendarDialogClosedMsg is emitted when the user cancels the dialog.
type CalendarDialogClosedMsg struct{}

// Form field indices for the calendar metadata fields. Index 4 is an empty
// spacer. In edit mode index 5 is the Display Calendar checkbox. Index 6+
// holds the Location/Account row and (for remote calendars) sync-health
// lines. Those later indices are dynamic. Only the metadata fields below
// get named constants.
const (
	cdIdxName        = 0
	cdIdxColor       = 1
	cdIdxDescription = 2
	cdIdxEmail       = 3
)

const (
	calDAVIdxServer = iota
	calDAVIdxUsername
	calDAVIdxAuth
	calDAVIdxSecret
	calDAVIdxAllowInsecure
)

const (
	calDAVIdxOAuthClientID      = calDAVIdxSecret
	calDAVIdxOAuthClientSecret  = calDAVIdxAllowInsecure
	calDAVIdxOAuthAllowInsecure = calDAVIdxAllowInsecure + 1
)

var authOptions = []SelectOption{
	{Label: "Basic", Value: "basic"},
	{Label: "Bearer", Value: "bearer"},
	{Label: "Google OAuth", Value: "oauth2"},
}

func authOptionIndex(authType string) int {
	at := strings.ToLower(strings.TrimSpace(authType))
	for i, opt := range authOptions {
		if opt.Value == at {
			return i
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// CalendarDialogModel
// ---------------------------------------------------------------------------

// CalendarDialogModel is a modal dialog to create or edit a calendar.
type CalendarDialogModel struct {
	id           int64
	name         string
	linked       bool
	dialog       Dialog
	form         Form
	body         viewport.Model
	help         help.Model
	testStatus   string
	theme        Theme
	accentColor  color.Color
	mutedColor   color.Color
	textDimColor color.Color

	// saveMakeDefault is shared by reference with the OnSubmit closure.
	// The "Save and Set as Default" path can then flip the MakeDefault
	// bit on the upcoming CalendarSavedMsg. It does not re-implement
	// form validation. Cleared automatically after each submit.
	saveMakeDefault   *bool
	accountConnection bool
	localDraft        *CalendarDialogParams
	discoveryPicker   *AccountCalendarPickerModel

	// contentWidth is shared with static sync-health rows so long errors
	// truncate to the dialog width instead of wrapping inside the box.
	contentWidth *int

	// hidden is the calendar's current visibility, mirrored into the Display
	// Calendar checkbox. The checkbox toggle updates this immediately and
	// emits CalendarVisibilityToggledMsg with the desired state; metadata
	// Save/Cancel never persists visibility.
	hidden bool
	// visibilityCb is the Display Calendar checkbox, nil in create mode.
	visibilityCb *CheckboxField
	// accountOpener is the actionable "Account: <name> ›" field for remote
	// calendars, nil for local calendars and create mode. Enter on it emits
	// AccountSettingsRequestedMsg so the owning manager/host can drill in.
	accountOpener *OpenerField
}

// isLocalhostHTTP reports whether a URL uses http:// against localhost
// or 127.0.0.1, in which case the dialog auto-enables the insecure flag.
func isLocalhostHTTP(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(s, "http://") {
		return false
	}
	host := strings.TrimPrefix(s, "http://")
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1"
}

// syncHealthLine is one row of the linked dialog's sync summary: raw text
// plus the style to apply after width truncation. Style happens at render
// time (inside the StaticField's styleFn). The text can then be truncated
// to the dialog's content width without a cut through ANSI escapes.
type syncHealthLine struct {
	text  string
	style lipgloss.Style
}

// syncHealthDialogLines renders compact account sync health for the calendar:
// a loud error plus an Account Settings remedy, or a quiet last-sync line.
func syncHealthDialogLines(params CalendarDialogParams, theme Theme) []syncHealthLine {
	if !params.RemoteLinked {
		return nil
	}
	if params.LastSyncError != "" {
		lines := []syncHealthLine{{
			text:  "⚠ Sync failed: " + humanizeSyncError(params.LastSyncError),
			style: lipgloss.NewStyle().Foreground(theme.Error),
		}}
		if hint := reLinkHint(params); hint != "" {
			lines = append(lines, syncHealthLine{text: hint, style: lipgloss.NewStyle().Foreground(theme.Muted)})
		}
		return lines
	}
	if params.LastSyncAt != "" {
		return []syncHealthLine{{
			text:  "Last synced: " + formatSyncTime(params.LastSyncAt),
			style: lipgloss.NewStyle().Foreground(theme.Muted),
		}}
	}
	return nil
}

// humanizeSyncError condenses a raw sync error into one readable line. Google's
// invalid_grant (expired/revoked OAuth refresh token) is the common case worth
// a rewrite. Everything else falls back to the first line of the raw error.
func humanizeSyncError(raw string) string {
	if strings.Contains(raw, "invalid_grant") {
		// Short on purpose. The hint line right below carries the action.
		// The dialog line must fit ~56 cols after the
		// "⚠ Sync failed: " prefix.
		return "Google login expired"
	}
	line := raw
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	const maxLen = 80
	if r := []rune(line); len(r) > maxLen {
		line = string(r[:maxLen-1]) + "…"
	}
	return line
}

// reLinkHint points credential failures to the account-level repair surface.
func reLinkHint(params CalendarDialogParams) string {
	if strings.Contains(params.LastSyncError, "invalid_grant") {
		return "Manage Account to sign in again."
	}
	return ""
}

// formatSyncTime renders an RFC 3339 timestamp as a compact local-ish line.
// Falls back to the raw value if it doesn't parse.
func formatSyncTime(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.Format("2006-01-02 15:04 MST")
}

func newRemoteURLField(value string) *TextField {
	f := NewTextField("https://cal.example.com/dav/calendars/work/")
	f.SetValue(value)
	f.SetCharLimit(512)
	return f
}

func newUsernameField(value string) *TextField {
	f := NewTextField("you@example.com")
	f.SetValue(value)
	f.SetCharLimit(256)
	return f
}

func newAuthField(authType string) *SelectField {
	f := NewSelectField(authOptions)
	f.SetSelected(authOptionIndex(authType))
	return f
}

func newPasswordField() *TextField {
	f := NewTextField("your password")
	f.SetCharLimit(256)
	f.SetEchoPassword(true)
	return f
}

// calendarAuthIsOAuth reports whether an auth-type string selects the OAuth
// flow (and therefore the ClientID/Secret tail layout in the dialog).
func calendarAuthIsOAuth(authType string) bool {
	return strings.EqualFold(strings.TrimSpace(authType), "oauth2")
}

// accountAuthIsBasicOrBearer reports whether an auth-type string selects an
// in-place credential update. Those are the non-OAuth flows whose secret
// (password or bearer token) can be rotated with no second discovery or
// re-consent.
func accountAuthIsBasicOrBearer(authType string) bool {
	t := strings.ToLower(strings.TrimSpace(authType))
	return t == "basic" || t == "bearer"
}

// accountAuthIsBearer reports whether the rotated secret is a bearer token
// (stored in AccessToken) rather than a password. The dialog's field label
// and the credential write must agree on this split, so both use this
// single predicate.
func accountAuthIsBearer(authType string) bool {
	return strings.EqualFold(strings.TrimSpace(authType), "bearer")
}

func newOAuthClientIDField(value string) *TextField {
	f := NewTextField("xxxx.apps.googleusercontent.com")
	f.SetValue(value)
	f.SetCharLimit(256)
	return f
}

func newOAuthClientSecretField() *TextField {
	f := NewTextField("paste your client secret")
	f.SetCharLimit(256)
	f.SetEchoPassword(true)
	return f
}

func (m CalendarDialogModel) ShowDiscovery(discovery account.Discovery) CalendarDialogModel {
	picker := NewAccountCalendarPickerModel(discovery, m.theme).
		SetSize(m.dialog.width, m.dialog.height)
	m.discoveryPicker = &picker
	return m
}

func (m CalendarDialogModel) HideDiscovery() CalendarDialogModel {
	m.discoveryPicker = nil
	return m
}

// SetAccountName refreshes account context with no form rebuild. In-progress
// calendar metadata edits then survive an account rename. It updates
// the inline "Account: <name> ›" opener in place.
func (m CalendarDialogModel) SetAccountName(name string) CalendarDialogModel {
	if !m.linked || m.localDraft == nil || m.accountOpener == nil {
		return m
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Connected account"
	}
	m.localDraft.AccountName = name
	m.accountOpener.SetValue(name + " ›")
	return m
}

// Draft returns the calendar's current editable state as params. That is
// the live field values plus the original context (ID, account linkage,
// sync health) and the current visibility. Hosts use it to keep an unsaved
// calendar draft across a drill into the owning account.
func (m CalendarDialogModel) Draft() CalendarDialogParams {
	if m.localDraft == nil {
		return CalendarDialogParams{}
	}
	draft := *m.localDraft
	if m.form.ItemCount() > cdIdxEmail {
		draft.Name = strings.TrimSpace(m.form.Field(cdIdxName).(*TextField).Value())
		draft.Color = strings.TrimSpace(m.form.Field(cdIdxColor).(*ColorField).Value())
		draft.Description = strings.TrimSpace(m.form.Field(cdIdxDescription).(*TextField).Value())
		draft.OwnerEmail = strings.TrimSpace(m.form.Field(cdIdxEmail).(*TextField).Value())
	}
	draft.Hidden = m.hidden
	return draft
}

// Hidden reports the calendar detail's current visibility state.
func (m CalendarDialogModel) Hidden() bool { return m.hidden }

// SetHidden mirrors a visibility state into the detail and its Display
// Calendar checkbox. It does not emit a toggle message.
func (m CalendarDialogModel) SetHidden(h bool) CalendarDialogModel {
	m.hidden = h
	if m.visibilityCb != nil {
		m.visibilityCb.SetChecked(!h)
	}
	return m
}

// leftMovesCursor reports whether the Left arrow would edit the focused field
// (a text or color input) rather than navigate. The calendar manager can
// then leave Left for the field while the user edits. Buttons
// and non-edit fields (checkbox, opener) leave Left free to pop.
// dirtyMetadata reports whether any editable metadata field differs from the
// values the detail opened with, i.e. whether an unsaved draft exists. The
// Display checkbox is excluded. Visibility commits immediately and is never
// part of the draft. Hosts use this so navigation gestures do not
// discard typed edits in silence.
func (m CalendarDialogModel) dirtyMetadata() bool {
	// The account-connection layout has different fields at these indices;
	// its cancel flow never prompts.
	if m.accountConnection || m.localDraft == nil || m.form.ItemCount() <= cdIdxEmail {
		return false
	}
	name, okName := m.form.Field(cdIdxName).(*TextField)
	colorField, okColor := m.form.Field(cdIdxColor).(*ColorField)
	desc, okDesc := m.form.Field(cdIdxDescription).(*TextField)
	email, okEmail := m.form.Field(cdIdxEmail).(*TextField)
	if !okName || !okColor || !okDesc || !okEmail {
		return false
	}
	return strings.TrimSpace(name.Value()) != strings.TrimSpace(m.localDraft.Name) ||
		strings.TrimSpace(colorField.Value()) != strings.TrimSpace(m.localDraft.Color) ||
		strings.TrimSpace(desc.Value()) != strings.TrimSpace(m.localDraft.Description) ||
		strings.TrimSpace(email.Value()) != strings.TrimSpace(m.localDraft.OwnerEmail)
}

// absorbsBack reports whether the pushed detail owns the Left key entirely.
// That is true while a text/color field moves its cursor, while the
// account-connection layout is active (its fields own Left), or while
// unsaved edits exist. A navigation gesture must never discard a draft.
// The host's Back gesture pops only when this is false.
func (m CalendarDialogModel) absorbsBack() bool {
	return m.leftMovesCursor() || m.accountConnection || m.dirtyMetadata()
}

// absorbsTab reports whether Tab traversal stays inside the detail. The
// account-connection layout and an open discovery picker own Tab outright.
// A dirty draft keeps wrap internally so traversal can never discard
// typed edits. The host's boundary hand-off fires only when this is false.
func (m CalendarDialogModel) absorbsTab() bool {
	return m.accountConnection || m.discoveryPicker != nil || m.dirtyMetadata()
}

func (m CalendarDialogModel) leftMovesCursor() bool {
	f := m.form
	if f.Focused() >= f.ItemCount() {
		return false
	}
	switch f.Field(f.Focused()).(type) {
	case *TextField, *ColorField:
		return true
	default:
		return false
	}
}
