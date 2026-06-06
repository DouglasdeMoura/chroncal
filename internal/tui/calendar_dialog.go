package tui

import (
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// CalendarDialogParams seeds the calendar dialog. All fields are optional;
// ID == 0 means "create a new calendar", and RemoteLinked reflects whether
// the calendar is currently connected to a remote CalDAV account.
type CalendarDialogParams struct {
	ID           int64
	Name         string
	Color        string // hex like "#a6e3a1"
	Description  string
	OwnerEmail   string
	RemoteURL    string
	RemoteLinked bool

	// RemoteAuthType and RemoteUsername are display-only; populated when
	// the calendar is linked so the dialog can show connection details.
	RemoteAuthType string
	RemoteUsername string

	// LastSyncAt, LastSyncAttemptedAt, and LastSyncError are display-only sync
	// health, populated when the calendar is linked. A non-empty LastSyncError
	// is the "why" behind the sidebar ⚠ marker; the dialog surfaces it here so
	// the user can read the reason and the fix one keystroke from the list.
	LastSyncAt          string // RFC 3339, empty when never synced cleanly
	LastSyncAttemptedAt string // RFC 3339, empty when never attempted
	LastSyncError       string

	// IsDefault marks the calendar being edited as the current default. It
	// drives the dialog's "Default calendar" badge and hides the redundant
	// Set-as-Default action. Ignored in create mode.
	IsDefault bool

	// OfferDefault enables the "Set as default after saving" checkbox in
	// create mode. Callers set it when at least one calendar already
	// exists, since the first calendar is auto-promoted by the service
	// (the checkbox would be meaningless and noisy in that case).
	OfferDefault bool

	// NeedOAuthConfig opens a linked OAuth calendar's dialog with editable
	// Client ID / Client secret rows. Used when re-authentication finds the
	// stored credential incomplete (linked before the client secret was
	// persisted). OAuthClientIDPrefill seeds the Client ID row when the
	// stored credential has the ID but not the secret.
	NeedOAuthConfig      bool
	OAuthClientIDPrefill string
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

	// Remote connection — only meaningful when RemoteURL is non-empty and
	// the calendar is not already linked.
	RemoteURL     string
	Username      string
	AuthType      string // "basic" | "bearer" | "oauth2"
	Password      string // basic: password; bearer: access token
	AllowInsecure bool

	// OAuth client config — populated only when AuthType == "oauth2". The
	// parent launches the browser authorization flow with these before
	// linking the calendar.
	OAuthClientID     string
	OAuthClientSecret string
}

// CalendarReauthRequestedMsg is emitted when the user presses Re-authenticate
// on a linked OAuth calendar. ClientID/ClientSecret are set only by the
// missing-config fallback (credentials stored before the client secret was
// kept); empty means "use the stored credential's client config".
type CalendarReauthRequestedMsg struct {
	ID           int64
	Name         string
	ClientID     string
	ClientSecret string
}

// CalendarDeleteRequestedMsg is emitted when the user presses Delete in the
// dialog. The parent is responsible for showing the confirm dialog.
type CalendarDeleteRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarDisconnectRemoteRequestedMsg is emitted when the user presses the
// Disconnect button in the dialog. The parent tears down the remote link.
type CalendarDisconnectRemoteRequestedMsg struct {
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
// button so the dialog can read the current field values before asking
// the parent to perform the actual connection check.
type testConnectionPressedMsg struct{}

// calendarSavePromotePressedMsg is an internal sentinel emitted by the
// "Save and Set as Default" button. The dialog catches it, sets the
// makeDefault flag observed by OnSubmit, and triggers the normal Save
// pipeline so the same validation runs.
type calendarSavePromotePressedMsg struct{}

// CalendarDialogClosedMsg is emitted when the user cancels the dialog.
type CalendarDialogClosedMsg struct{}

// Form field indices. Local fields are always present. Index 4 is an empty
// spacer row; index 5 is the Sync toggle in unlinked mode or a read-only
// status line in linked mode. Remote fields (6..11) exist only when Sync
// is on. The "Same as owner email" mirror lives inside the sync section
// so it's only visible when a CalDAV connection is being configured.
const (
	cdIdxName        = 0
	cdIdxColor       = 1
	cdIdxDescription = 2
	cdIdxEmail       = 3
	// Index 4 is an empty spacer StaticField.
	cdIdxSync = 5

	// Present only when Sync is on (unlinked mode only). Rows 10+ depend
	// on the selected auth type; the constants below describe the two
	// layouts. Form.RemoveItems is tail-truncation only, so switching
	// layouts rebuilds everything from cdIdxPassword down (see OnRebuild).
	cdIdxRemoteURL       = 6
	cdIdxUsername        = 7
	cdIdxSameAsOwnerMail = 8
	cdIdxAuth            = 9

	// basic/bearer layout:
	cdIdxPassword      = 10
	cdIdxAllowInsecure = 11

	// oauth2 layout (replaces the Password row with two client-config rows,
	// shifting the HTTP checkbox down one):
	cdIdxOAuthClientID      = 10
	cdIdxOAuthClientSecret  = 11
	cdIdxOAuthAllowInsecure = 12
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

// CalendarDialogModel is a modal dialog for creating/editing a calendar.
type CalendarDialogModel struct {
	id           int64
	name         string
	linked       bool
	dialog       Dialog
	form         Form
	help         help.Model
	testStatus   string
	theme        Theme
	accentColor  color.Color
	mutedColor   color.Color
	textDimColor color.Color

	// saveMakeDefault is shared by reference with the OnSubmit closure so
	// the "Save and Set as Default" path can flip the MakeDefault bit on
	// the upcoming CalendarSavedMsg without re-implementing form
	// validation. Cleared automatically after each submit.
	saveMakeDefault *bool
}

// NewCalendarDialogModel builds a dialog for create (params.ID==0) or edit.
func NewCalendarDialogModel(params CalendarDialogParams, theme Theme) CalendarDialogModel {
	title := "New calendar"
	if params.ID > 0 {
		title = "Edit calendar"
		// Apple's "Get Info" sheet shows the default badge inline with the
		// title — readable at a glance and impossible to confuse with an
		// editable field. We do the same so users opening a calendar via
		// the sidebar's Return keypress see the state immediately.
		if params.IsDefault {
			title += " · Default"
		}
	}
	if params.Color == "" {
		params.Color = "#a6e3a1"
	}

	styles := DefaultDialogStyles()
	dialog := NewDialog(title, styles)
	dialog.SetWidth(62)

	formStyles := DefaultFormStyles()
	formStyles.LabelLayout = LabelInline
	formStyles.ShowFocusMarker = true
	formStyles.ButtonAlign = ButtonAlignRight
	formStyles.ButtonRule = true

	nameField := NewTextField("e.g. Work")
	nameField.SetValue(params.Name)
	nameField.SetCharLimit(256)

	colorField := NewColorField(theme.CalendarSwatches, params.Color, theme.TextDim)

	descField := NewTextField("Shared family schedule")
	descField.SetValue(params.Description)
	descField.SetCharLimit(500)

	emailField := NewTextField("you@example.com")
	emailField.SetValue(params.OwnerEmail)
	emailField.SetCharLimit(256)

	items := []FormItem{
		{Label: "Name", Field: nameField, Required: true},
		{Label: "Color", Field: colorField, Required: true},
		{Label: "Description", Field: descField},
		{Label: "Owner email", Field: emailField},
		{Label: "", Field: NewStaticField("", nil)},
	}

	// Fallback config fields for re-auth on a credential that predates
	// client-secret storage. Built up front so the Re-authenticate button
	// closure can read them at press time without reaching into the form.
	var (
		oauthIDField     *TextField
		oauthSecretField *TextField
	)

	if params.RemoteLinked {
		summary := remoteStatusLine(params, theme)
		items = append(items, FormItem{
			Label: "",
			Field: NewStaticField(summary, nil),
		})
		// Surface sync health (one static field per line so the form's
		// height math stays one-line-per-item). Empty when the calendar has
		// synced cleanly and never been attempted-with-error.
		for _, line := range syncHealthDialogLines(params, theme) {
			items = append(items, FormItem{Label: "", Field: NewStaticField(line, nil)})
		}
		if params.NeedOAuthConfig {
			hint := lipgloss.NewStyle().Foreground(theme.Muted).
				Render("Stored credential is missing the OAuth client config — enter it once to re-authenticate.")
			oauthIDField = newOAuthClientIDField(params.OAuthClientIDPrefill)
			oauthSecretField = newOAuthClientSecretField()
			items = append(items,
				FormItem{Label: "", Field: NewStaticField(hint, nil)},
				FormItem{Label: "Client ID", Field: oauthIDField, Required: true},
				FormItem{Label: "Client secret", Field: oauthSecretField, Required: true},
			)
		}
	} else {
		sync := NewCheckboxField("", false)
		sync.SetContent("Enable CalDAV sync")
		items = append(items, FormItem{Label: "Sync", Field: sync})
	}

	form := NewForm("Save", formStyles, items...)

	savedID := params.ID
	linked := params.RemoteLinked
	saveMakeDefault := new(bool)
	form.OnSubmit(func(f *Form) tea.Cmd {
		nameVal := strings.TrimSpace(f.Field(cdIdxName).(*TextField).Value())
		hexVal := strings.TrimSpace(f.Field(cdIdxColor).(*ColorField).Value())
		descVal := strings.TrimSpace(f.Field(cdIdxDescription).(*TextField).Value())
		emailVal := strings.TrimSpace(f.Field(cdIdxEmail).(*TextField).Value())

		msg := CalendarSavedMsg{
			ID:          savedID,
			Name:        nameVal,
			Color:       hexVal,
			Description: descVal,
			OwnerEmail:  emailVal,
			MakeDefault: *saveMakeDefault,
		}
		*saveMakeDefault = false

		if !linked && syncEnabled(f) {
			urlVal := strings.TrimSpace(f.Field(cdIdxRemoteURL).(*TextField).Value())
			userVal := strings.TrimSpace(f.Field(cdIdxUsername).(*TextField).Value())
			authVal := f.Field(cdIdxAuth).(*SelectField).Value()

			if urlVal == "" {
				f.SetError(cdIdxRemoteURL, "Remote URL is required when Sync is on")
				return nil
			}
			if userVal == "" {
				f.SetError(cdIdxUsername, "Username is required when Sync is on")
				return nil
			}

			// The tail rows depend on the auth type (see the cdIdx*
			// comment); authVal is the layout's source of truth here
			// because any auth change fires OnRebuild before Submit.
			if calendarAuthIsOAuth(authVal) {
				clientID := strings.TrimSpace(f.Field(cdIdxOAuthClientID).(*TextField).Value())
				clientSecret := strings.TrimSpace(f.Field(cdIdxOAuthClientSecret).(*TextField).Value())
				if clientID == "" {
					f.SetError(cdIdxOAuthClientID, "Client ID is required for Google OAuth")
					return nil
				}
				if clientSecret == "" {
					f.SetError(cdIdxOAuthClientSecret, "Client secret is required for Google OAuth")
					return nil
				}
				msg.OAuthClientID = clientID
				msg.OAuthClientSecret = clientSecret
				msg.AllowInsecure = f.Field(cdIdxOAuthAllowInsecure).(*CheckboxField).Checked()
			} else {
				passVal := f.Field(cdIdxPassword).(*TextField).Value()
				if passVal == "" {
					if authVal == "bearer" {
						f.SetError(cdIdxPassword, "Access token is required for bearer auth")
					} else {
						f.SetError(cdIdxPassword, "Password is required for basic auth")
					}
					return nil
				}
				msg.Password = passVal
				msg.AllowInsecure = f.Field(cdIdxAllowInsecure).(*CheckboxField).Checked()
			}

			msg.RemoteURL = urlVal
			msg.Username = userVal
			msg.AuthType = authVal
		}

		return func() tea.Msg { return msg }
	})

	form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return CalendarDialogClosedMsg{} }
	})

	m := CalendarDialogModel{
		id:              params.ID,
		name:            params.Name,
		linked:          params.RemoteLinked,
		dialog:          dialog,
		form:            form,
		help:            newThemedHelp(theme),
		theme:           theme,
		accentColor:     theme.Selected,
		mutedColor:      theme.Muted,
		textDimColor:    theme.TextDim,
		saveMakeDefault: saveMakeDefault,
	}

	// Edit mode, not yet default: surface "Set as Default" so the user
	// can reach the action without backing out into the manage-calendars
	// list. Hidden when already default — no valid "unset" exists.
	// Registered before Disconnect so Tab order is benign-then-destructive
	// (and visually Set as Default sits left of Disconnect on the leading
	// side) — a reflex Tab from Save should never land on a destructive
	// action first.
	if params.ID > 0 && !params.IsDefault {
		id := params.ID
		name := params.Name
		form.SetLeadingActionButton("Set as Default", Button, func() tea.Msg {
			return CalendarSetDefaultRequestedMsg{ID: id, Name: name}
		})
	}

	// Linked OAuth calendars get Re-authenticate, registered before
	// Disconnect so Tab order stays benign-then-destructive. When the
	// dialog is in the missing-config fallback mode, the button reads the
	// client config fields at press time; otherwise it sends empty config
	// and the parent uses the stored credential.
	if params.RemoteLinked && calendarAuthIsOAuth(params.RemoteAuthType) {
		id := params.ID
		name := params.Name
		idField, secretField := oauthIDField, oauthSecretField
		form.SetLeadingActionButton("Re-authenticate", Button, func() tea.Msg {
			msg := CalendarReauthRequestedMsg{ID: id, Name: name}
			if idField != nil {
				msg.ClientID = strings.TrimSpace(idField.Value())
			}
			if secretField != nil {
				msg.ClientSecret = strings.TrimSpace(secretField.Value())
			}
			return msg
		})
	}

	if params.RemoteLinked {
		id := params.ID
		name := params.Name
		form.SetLeadingActionButton("Disconnect", ButtonDanger, func() tea.Msg {
			return CalendarDisconnectRemoteRequestedMsg{ID: id, Name: name}
		})
	}

	// Create mode with at least one calendar already on disk: offer to
	// promote the new row to default in one save, instead of forcing a
	// follow-up trip through the list dialog. Suppressed for the very
	// first calendar since the service auto-promotes that row silently.
	if params.ID == 0 && params.OfferDefault {
		form.SetLeadingActionButton("Save and Set as Default", Button, func() tea.Msg {
			return calendarSavePromotePressedMsg{}
		})
	}

	syncTheme := theme
	// Snapshot of the remote section values, preserved across Sync toggles
	// and auth-layout switches so flipping things back and forth doesn't
	// wipe what the user has already typed.
	var snap struct {
		url, username, auth, password    string
		oauthClientID, oauthClientSecret string
		allowInsecure, sameAsOwner       bool
	}
	// oauthLayout tracks which tail layout the form currently has (see the
	// cdIdx* comment): false = Password+HTTP, true = ClientID+Secret+HTTP.
	// Form.RemoveItems is tail-truncation only, so layout changes rebuild
	// from cdIdxPassword down rather than swapping a row in place.
	oauthLayout := new(bool)

	// appendAuthTail appends the rows after the Auth select for the given
	// auth type and updates the layout tracker.
	appendAuthTail := func(f *Form, authVal string) {
		insecure := NewCheckboxField("", snap.allowInsecure)
		insecure.SetContent("allow plain HTTP")
		if calendarAuthIsOAuth(authVal) {
			secret := newOAuthClientSecretField()
			secret.SetValue(snap.oauthClientSecret)
			f.AppendItems(
				FormItem{Label: "Client ID", Field: newOAuthClientIDField(snap.oauthClientID), Required: true},
				FormItem{Label: "Client secret", Field: secret, Required: true},
				FormItem{Label: "HTTP", Field: insecure},
			)
			*oauthLayout = true
			return
		}
		password := newPasswordField()
		password.SetValue(snap.password)
		f.AppendItems(
			FormItem{Label: "Password", Field: password, Required: true},
			FormItem{Label: "HTTP", Field: insecure},
		)
		*oauthLayout = false
	}

	// snapshotAuthTail records the current tail values, layout-aware.
	snapshotAuthTail := func(f *Form) {
		if *oauthLayout {
			snap.oauthClientID = f.Field(cdIdxOAuthClientID).(*TextField).Value()
			snap.oauthClientSecret = f.Field(cdIdxOAuthClientSecret).(*TextField).Value()
			snap.allowInsecure = f.Field(cdIdxOAuthAllowInsecure).(*CheckboxField).Checked()
			return
		}
		snap.password = f.Field(cdIdxPassword).(*TextField).Value()
		snap.allowInsecure = f.Field(cdIdxAllowInsecure).(*CheckboxField).Checked()
	}

	form.OnRebuild(func(f *Form) {
		if linked {
			return
		}
		syncOn := syncEnabled(f)
		hasRemote := f.ItemCount() > cdIdxSync+1
		switch {
		case syncOn && !hasRemote:
			f.AppendItems(
				FormItem{Label: "Remote URL", Field: newRemoteURLField(snap.url), Required: true},
				FormItem{Label: "Username", Field: newMirroredUsernameField(snap.username, syncTheme), Required: true},
				FormItem{Label: " ", Field: newSameAsOwnerMailCheckbox(snap.sameAsOwner)},
				FormItem{Label: "Auth", Field: newAuthField(snap.auth)},
			)
			appendAuthTail(f, snap.auth)
			f.SetActionButton("Test", Button, func() tea.Msg {
				return testConnectionPressedMsg{}
			})
		case !syncOn && hasRemote:
			snap.url = f.Field(cdIdxRemoteURL).(*TextField).Value()
			snap.username = f.Field(cdIdxUsername).(*TextField).Value()
			snap.auth = f.Field(cdIdxAuth).(*SelectField).Value()
			snap.sameAsOwner = f.Field(cdIdxSameAsOwnerMail).(*CheckboxField).Checked()
			snapshotAuthTail(f)
			f.RemoveItems(cdIdxSync + 1)
			f.ClearError()
			f.ClearActionButtons()
		}

		// Rebuild the tail when the selected auth type changes layout
		// (basic/bearer <-> oauth2).
		if syncOn && f.ItemCount() > cdIdxAuth {
			authVal := f.Field(cdIdxAuth).(*SelectField).Value()
			if calendarAuthIsOAuth(authVal) != *oauthLayout {
				snapshotAuthTail(f)
				f.RemoveItems(cdIdxPassword)
				f.ClearError()
				appendAuthTail(f, authVal)
			}
		}

		// Keep the Password row's label and placeholder in sync with the
		// selected auth type: basic -> password, bearer -> access token.
		// (The oauth2 layout has no Password row.)
		if syncOn && !*oauthLayout && f.ItemCount() > cdIdxPassword {
			authVal := f.Field(cdIdxAuth).(*SelectField).Value()
			pw := f.Field(cdIdxPassword).(*TextField)
			if authVal == "bearer" {
				f.SetItemLabel(cdIdxPassword, "Token")
				pw.SetPlaceholder("paste your API token")
			} else {
				f.SetItemLabel(cdIdxPassword, "Password")
				pw.SetPlaceholder("your password")
			}
		}

		// Auto-enable HTTP (insecure) for localhost URLs so casual dev use
		// doesn't require the flag. Shown as a greyed-out confirmation line.
		// When the URL stops matching localhost the override is cleared so
		// the user re-opts-in explicitly.
		insecureIdx := cdIdxAllowInsecure
		if *oauthLayout {
			insecureIdx = cdIdxOAuthAllowInsecure
		}
		if syncOn && f.ItemCount() > insecureIdx {
			urlVal := strings.TrimSpace(f.Field(cdIdxRemoteURL).(*TextField).Value())
			insecure := f.Field(insecureIdx).(*CheckboxField)
			wasAuto := insecure.AutoChecked()
			if isLocalhostHTTP(urlVal) {
				insecure.SetChecked(true)
				insecure.SetAutoChecked(true)
				insecure.SetSuffix("")
				insecure.SetDisabledWhen(func() (bool, string) {
					return true, lipgloss.NewStyle().Foreground(syncTheme.Muted).Italic(true).
						Render("auto-enabled for localhost")
				})
			} else {
				if wasAuto {
					insecure.SetChecked(false)
					insecure.SetAutoChecked(false)
				}
				insecure.SetDisabledWhen(nil)
				if insecure.Checked() {
					insecure.SetSuffix(lipgloss.NewStyle().
						Foreground(syncTheme.Error).
						Render("(unencrypted)"))
				} else {
					insecure.SetSuffix("")
				}
			}
		}

		// Mirror the owner email into the CalDAV username when the
		// "Same as owner email" checkbox is checked. Runs last so the
		// username field is guaranteed present when sync just went on.
		applySameAsOwnerMail(f)
	})
	m.form = form

	return m
}

// applySameAsOwnerMail drives the "Same as owner email" checkbox: when
// checked with a non-empty Owner email, the CalDAV Username field is
// pinned to that value and rendered as disabled. The checkbox itself is
// always interactive and never has its styling altered by this helper.
func applySameAsOwnerMail(f *Form) {
	if f.ItemCount() <= cdIdxSameAsOwnerMail {
		return
	}
	username, ok := f.Field(cdIdxUsername).(*TextField)
	if !ok {
		return
	}
	email, ok := f.Field(cdIdxEmail).(*TextField)
	if !ok {
		return
	}
	box, ok := f.Field(cdIdxSameAsOwnerMail).(*CheckboxField)
	if !ok {
		return
	}

	emailVal := strings.TrimSpace(email.Value())
	if box.Checked() && emailVal != "" {
		username.SetValue(emailVal)
		username.SetDisabled(true)
	} else {
		username.SetDisabled(false)
	}
}

// syncEnabled reports whether the Sync checkbox is currently on. Returns
// false in linked mode (where the checkbox doesn't exist).
func syncEnabled(f *Form) bool {
	if f.ItemCount() <= cdIdxSync {
		return false
	}
	cb, ok := f.Field(cdIdxSync).(*CheckboxField)
	if !ok {
		return false
	}
	return cb.Checked()
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

func remoteStatusLine(params CalendarDialogParams, theme Theme) string {
	label := lipgloss.NewStyle().Foreground(theme.Muted).Render("Remote:")
	details := params.RemoteURL
	if params.RemoteUsername != "" {
		details += "  (" + params.RemoteUsername
		if params.RemoteAuthType != "" {
			details += ", " + params.RemoteAuthType
		}
		details += ")"
	} else if params.RemoteAuthType != "" {
		details += "  (" + params.RemoteAuthType + ")"
	}
	return label + " " + details
}

// syncHealthDialogLines renders the calendar's sync health for the dialog: a
// loud error line plus an actionable re-link hint when the last sync failed, or
// a quiet "Last synced" line otherwise. Returns nil for unlinked calendars or
// linked-but-never-attempted ones (nothing useful to say yet).
func syncHealthDialogLines(params CalendarDialogParams, theme Theme) []string {
	if !params.RemoteLinked {
		return nil
	}
	if params.LastSyncError != "" {
		errStyle := lipgloss.NewStyle().Foreground(theme.Error)
		hintStyle := lipgloss.NewStyle().Foreground(theme.Muted)
		lines := []string{errStyle.Render("⚠ Last sync failed: " + humanizeSyncError(params.LastSyncError))}
		if hint := reLinkHint(params); hint != "" {
			lines = append(lines, hintStyle.Render(hint))
		}
		return lines
	}
	if params.LastSyncAt != "" {
		return []string{lipgloss.NewStyle().Foreground(theme.Muted).Render("Last synced: " + formatSyncTime(params.LastSyncAt))}
	}
	return nil
}

// humanizeSyncError condenses a raw sync error into one readable line. Google's
// invalid_grant (expired/revoked OAuth refresh token) is the common case worth
// translating; everything else falls back to the first line of the raw error.
func humanizeSyncError(raw string) string {
	if strings.Contains(raw, "invalid_grant") {
		return "Google login expired — re-authentication needed"
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

// reLinkHint returns the fix for errors that need re-authentication, or "" when
// no specific remedy applies. The Re-authenticate action button re-runs the
// OAuth flow in-app and stores a fresh refresh token.
func reLinkHint(params CalendarDialogParams) string {
	if strings.Contains(params.LastSyncError, "invalid_grant") {
		if calendarAuthIsOAuth(params.RemoteAuthType) {
			return "Press Re-authenticate below to fix."
		}
		name := params.Name
		if name == "" {
			name = "<name>"
		}
		return "Re-link: chroncal calendar update " + name + " --auth oauth2"
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

// newSameAsOwnerMailCheckbox builds the "Same as owner email" mirror.
// Focus is quiet (no reverse highlight) because the field is secondary to
// the Username it drives.
func newSameAsOwnerMailCheckbox(checked bool) *CheckboxField {
	f := NewCheckboxField("", checked)
	f.SetContent("Same as owner email")
	f.SetQuietFocus(true)
	return f
}

// newMirroredUsernameField builds the Username TextField used inside the
// sync section. Its dim-style is set so the disabled state (driven by the
// "Same as owner email" checkbox) reads clearly.
func newMirroredUsernameField(value string, theme Theme) *TextField {
	f := newUsernameField(value)
	f.SetDimStyle(lipgloss.NewStyle().Foreground(theme.TextDim).Italic(true))
	return f
}

func (m CalendarDialogModel) SetSize(w, h int) CalendarDialogModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.form.SetWidth(m.dialog.ContentWidth())
	return m
}

func (m CalendarDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m CalendarDialogModel) Update(msg tea.Msg) (CalendarDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	if _, ok := msg.(testConnectionPressedMsg); ok {
		return m.handleTestPressed()
	}

	if _, ok := msg.(calendarSavePromotePressedMsg); ok {
		if m.saveMakeDefault != nil {
			*m.saveMakeDefault = true
		}
		var cmd tea.Cmd
		m.form, cmd = m.form.Submit()
		return m, cmd
	}

	if tr, ok := msg.(CalendarTestResultMsg); ok {
		if tr.OK {
			m.testStatus = lipgloss.NewStyle().Foreground(m.theme.Accent).
				Render("✓ " + tr.Message)
		} else {
			m.testStatus = lipgloss.NewStyle().Foreground(m.theme.Error).
				Render("✗ " + tr.Message)
		}
		return m, nil
	}

	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			return m, func() tea.Msg { return CalendarDialogClosedMsg{} }
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+s"))):
			var cmd tea.Cmd
			m.form, cmd = m.form.Submit()
			return m, cmd
		}
	}

	if mc, ok := msg.(tea.MouseClickMsg); ok {
		if mc.Button == tea.MouseLeft {
			bw, bh := m.BoxSize()
			ox := (m.dialog.width - bw) / 2
			oy := (m.dialog.height - bh) / 2
			target := mouseResolve(mc.X-ox, mc.Y-oy)
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(MouseEvent{IsClick: true, Target: target})
			return m, cmd
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m CalendarDialogModel) View() string {
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))
	body := m.form.View()
	if m.testStatus != "" {
		body += "\n" + m.testStatus
	}
	content := mouseSweep(m.dialog.Box(body))
	return content
}

// handleTestPressed validates the remote fields and, when they're
// populated, emits a CalendarTestRequestedMsg so the parent can run the
// authenticated ping. Errors show inline without contacting the server.
func (m CalendarDialogModel) handleTestPressed() (CalendarDialogModel, tea.Cmd) {
	if m.form.ItemCount() <= cdIdxAllowInsecure {
		return m, nil
	}
	// The oauth2 layout has no password to ping with — there is no token
	// until the browser flow runs, which happens on save.
	if calendarAuthIsOAuth(m.form.Field(cdIdxAuth).(*SelectField).Value()) {
		m.testStatus = lipgloss.NewStyle().Foreground(m.theme.TextDim).Italic(true).
			Render("Test runs after Google authorization — save to connect")
		return m, nil
	}
	url := strings.TrimSpace(m.form.Field(cdIdxRemoteURL).(*TextField).Value())
	user := strings.TrimSpace(m.form.Field(cdIdxUsername).(*TextField).Value())
	auth := m.form.Field(cdIdxAuth).(*SelectField).Value()
	pass := m.form.Field(cdIdxPassword).(*TextField).Value()
	ins := m.form.Field(cdIdxAllowInsecure).(*CheckboxField).Checked()

	if url == "" || user == "" || pass == "" {
		m.testStatus = lipgloss.NewStyle().Foreground(m.theme.Error).
			Render("✗ Fill URL, Username, and Password first")
		return m, nil
	}

	m.testStatus = lipgloss.NewStyle().Foreground(m.theme.TextDim).Italic(true).
		Render("Testing…")
	return m, func() tea.Msg {
		return CalendarTestRequestedMsg{
			URL:           url,
			Username:      user,
			AuthType:      auth,
			Password:      pass,
			AllowInsecure: ins,
		}
	}
}
