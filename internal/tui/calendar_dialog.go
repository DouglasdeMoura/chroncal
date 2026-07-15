package tui

import (
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
)

// CalendarDialogParams seeds the calendar dialog. All fields are optional;
// ID == 0 means "create a new calendar", and RemoteLinked reflects whether
// the calendar is currently connected to a remote CalDAV account.
type CalendarDialogParams struct {
	ID           int64
	AccountID    int64
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
}

// CalendarDiscoveryRequestedMsg starts account discovery from the integrated
// New Calendar flow. Remote collection metadata supplies the names, colors,
// and descriptions for every selected calendar.
type CalendarDiscoveryRequestedMsg struct {
	ServerURL         string
	Username          string
	AuthType          string
	Secret            string
	OAuthClientID     string
	OAuthClientSecret string
	AllowInsecure     bool
}

// CalendarDiscoverAdditionalRequestedMsg rediscovers the account backing an
// edited remote calendar and opens the integrated collection picker.
type CalendarDiscoverAdditionalRequestedMsg struct {
	CalendarID int64
	AccountID  int64
}

type calendarConnectionBackMsg struct{}

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

// Form field indices for the local calendar form. Index 4 is an empty spacer
// row; index 5 is the CalDAV toggle in create mode or a read-only connection
// status line in linked mode.
const (
	cdIdxName        = 0
	cdIdxColor       = 1
	cdIdxDescription = 2
	cdIdxEmail       = 3
	// Index 4 is an empty spacer StaticField.
	cdIdxSync = 5
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

// CalendarDialogModel is a modal dialog for creating/editing a calendar.
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

	// saveMakeDefault is shared by reference with the OnSubmit closure so
	// the "Save and Set as Default" path can flip the MakeDefault bit on
	// the upcoming CalendarSavedMsg without re-implementing form
	// validation. Cleared automatically after each submit.
	saveMakeDefault *bool
	connectionMode  *bool
	localDraft      *CalendarDialogParams
	discoveryPicker *AccountCalendarPickerModel

	// contentWidth is shared with the static-line styleFns so long values
	// (remote URLs, sync errors) truncate to the dialog's content width at
	// render time instead of wrapping inside the box.
	contentWidth *int
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

	// contentWidth is shared with the static-line styleFns (same pattern as
	// ConfirmDialogModel): the dialog's content width isn't known until
	// SetSize, and long values (Google CalDAV URLs easily exceed 90 chars)
	// must truncate to one row instead of wrapping raggedly inside the box.
	contentWidth := new(int)

	// staticLine builds a one-row static field: truncate to the content
	// width first, then style — slicing after styling would cut through
	// ANSI escapes.
	staticLine := func(text string, style lipgloss.Style) FormItem {
		return FormItem{Label: "", Field: NewStaticField(text, func(s string) string {
			if *contentWidth > 0 {
				s = truncateTo(s, *contentWidth)
			}
			return style.Render(s)
		})}
	}
	// labeledLine keeps the muted "Label:" prefix two-tone while the value
	// truncates to whatever width remains beside it.
	labeledLine := func(label, value string) FormItem {
		lbl := lipgloss.NewStyle().Foreground(theme.Muted).Render(label) + " "
		lblW := lipgloss.Width(lbl)
		return FormItem{Label: "", Field: NewStaticField(value, func(s string) string {
			if avail := *contentWidth - lblW; *contentWidth > 0 && avail > 0 {
				s = truncateTo(s, avail)
			}
			return lbl + s
		})}
	}

	if params.RemoteLinked {
		// One row each, truncated — the URL and account can both exceed the
		// dialog width on Google calendars.
		items = append(items, labeledLine("Remote:", params.RemoteURL))
		if account := remoteAccountSummary(params); account != "" {
			items = append(items, labeledLine("Account:", account))
		}
		// Surface sync health (one static field per line so the form's
		// height math stays one-line-per-item). Empty when the calendar has
		// synced cleanly and never been attempted-with-error.
		for _, line := range syncHealthDialogLines(params, theme) {
			items = append(items, staticLine(line.text, line.style))
		}
		if params.NeedOAuthConfig {
			oauthIDField = newOAuthClientIDField(params.OAuthClientIDPrefill)
			oauthSecretField = newOAuthClientSecretField()
			items = append(items,
				staticLine("Enter the OAuth client config once to re-authenticate.",
					lipgloss.NewStyle().Foreground(theme.Muted)),
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

		return func() tea.Msg { return msg }
	})

	form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return CalendarDialogClosedMsg{} }
	})

	connectionMode := new(bool)
	localDraft := params

	m := CalendarDialogModel{
		id:              params.ID,
		name:            params.Name,
		linked:          params.RemoteLinked,
		dialog:          dialog,
		form:            form,
		body:            viewport.New(),
		help:            newThemedHelp(theme),
		theme:           theme,
		accentColor:     theme.Selected,
		mutedColor:      theme.Muted,
		textDimColor:    theme.TextDim,
		saveMakeDefault: saveMakeDefault,
		connectionMode:  connectionMode,
		localDraft:      &localDraft,
		contentWidth:    contentWidth,
	}
	m.body.MouseWheelEnabled = true

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

	if params.RemoteLinked && params.AccountID > 0 {
		calendarID := params.ID
		accountID := params.AccountID
		form.SetLeadingActionButton("Add calendars", Button, func() tea.Msg {
			return CalendarDiscoverAdditionalRequestedMsg{
				CalendarID: calendarID,
				AccountID:  accountID,
			}
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

	form.OnRebuild(func(f *Form) {
		if linked || !syncEnabled(f) {
			return
		}
		localDraft.Name = strings.TrimSpace(f.Field(cdIdxName).(*TextField).Value())
		localDraft.Color = strings.TrimSpace(f.Field(cdIdxColor).(*ColorField).Value())
		localDraft.Description = strings.TrimSpace(f.Field(cdIdxDescription).(*TextField).Value())
		localDraft.OwnerEmail = strings.TrimSpace(f.Field(cdIdxEmail).(*TextField).Value())
		*connectionMode = true
		connection := newCalDAVConnectionForm(theme, localDraft.OwnerEmail)
		connection.SetWidth(dialog.ContentWidth())
		*f = connection
	})
	m.form = form

	return m
}

func newCalDAVConnectionForm(theme Theme, usernamePrefill string) Form {
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelInline
	styles.ShowFocusMarker = true
	styles.ButtonAlign = ButtonAlignRight
	styles.ButtonRule = true

	insecure := NewCheckboxField("", false)
	insecure.SetContent("allow plain HTTP")
	form := NewForm("Discover calendars", styles,
		FormItem{Label: "Server URL", Field: newRemoteURLField(""), Required: true},
		FormItem{Label: "Username", Field: newUsernameField(usernamePrefill), Required: true},
		FormItem{Label: "Auth", Field: newAuthField("basic"), Required: true},
		FormItem{Label: "Password", Field: newPasswordField(), Required: true},
		FormItem{Label: "HTTP", Field: insecure},
	)
	form.SetLeadingActionButton("Back", Button, func() tea.Msg {
		return calendarConnectionBackMsg{}
	})
	form.SetActionButton("Test", Button, func() tea.Msg {
		return testConnectionPressedMsg{}
	})
	form.OnCancel(func(*Form) tea.Cmd {
		return func() tea.Msg { return CalendarDialogClosedMsg{} }
	})

	var snapshot struct {
		secret, clientID, clientSecret string
		allowInsecure                  bool
	}
	oauthLayout := new(bool)
	snapshotTail := func(f *Form) {
		if *oauthLayout {
			snapshot.clientID = f.Field(calDAVIdxOAuthClientID).(*TextField).Value()
			snapshot.clientSecret = f.Field(calDAVIdxOAuthClientSecret).(*TextField).Value()
			snapshot.allowInsecure = f.Field(calDAVIdxOAuthAllowInsecure).(*CheckboxField).Checked()
			return
		}
		snapshot.secret = f.Field(calDAVIdxSecret).(*TextField).Value()
		snapshot.allowInsecure = f.Field(calDAVIdxAllowInsecure).(*CheckboxField).Checked()
	}
	appendTail := func(f *Form, authType string) {
		allow := NewCheckboxField("", snapshot.allowInsecure)
		allow.SetContent("allow plain HTTP")
		if calendarAuthIsOAuth(authType) {
			clientSecret := newOAuthClientSecretField()
			clientSecret.SetValue(snapshot.clientSecret)
			f.AppendItems(
				FormItem{Label: "Client ID", Field: newOAuthClientIDField(snapshot.clientID), Required: true},
				FormItem{Label: "Client secret", Field: clientSecret, Required: true},
				FormItem{Label: "HTTP", Field: allow},
			)
			*oauthLayout = true
			return
		}
		secret := newPasswordField()
		secret.SetValue(snapshot.secret)
		f.AppendItems(
			FormItem{Label: "Password", Field: secret, Required: true},
			FormItem{Label: "HTTP", Field: allow},
		)
		*oauthLayout = false
	}
	form.OnRebuild(func(f *Form) {
		authType := f.Field(calDAVIdxAuth).(*SelectField).Value()
		if calendarAuthIsOAuth(authType) != *oauthLayout {
			snapshotTail(f)
			f.RemoveItems(calDAVIdxSecret)
			f.ClearError()
			appendTail(f, authType)
		}
		if !*oauthLayout {
			secret := f.Field(calDAVIdxSecret).(*TextField)
			if authType == "bearer" {
				f.SetItemLabel(calDAVIdxSecret, "Token")
				secret.SetPlaceholder("paste your API token")
			} else {
				f.SetItemLabel(calDAVIdxSecret, "Password")
				secret.SetPlaceholder("your password")
			}
		}

		insecureIdx := calDAVIdxAllowInsecure
		if *oauthLayout {
			insecureIdx = calDAVIdxOAuthAllowInsecure
		}
		allow := f.Field(insecureIdx).(*CheckboxField)
		wasAuto := allow.AutoChecked()
		if isLocalhostHTTP(f.Field(calDAVIdxServer).(*TextField).Value()) {
			allow.SetChecked(true)
			allow.SetAutoChecked(true)
			allow.SetSuffix("")
			allow.SetDisabledWhen(func() (bool, string) {
				return true, lipgloss.NewStyle().Foreground(theme.Muted).Italic(true).
					Render("auto-enabled for localhost")
			})
		} else {
			if wasAuto {
				allow.SetChecked(false)
				allow.SetAutoChecked(false)
			}
			allow.SetDisabledWhen(nil)
			if allow.Checked() {
				allow.SetSuffix(lipgloss.NewStyle().Foreground(theme.Error).Render("(unencrypted)"))
			} else {
				allow.SetSuffix("")
			}
		}
	})
	form.OnSubmit(func(f *Form) tea.Cmd {
		msg := CalendarDiscoveryRequestedMsg{
			ServerURL: strings.TrimSpace(f.Field(calDAVIdxServer).(*TextField).Value()),
			Username:  strings.TrimSpace(f.Field(calDAVIdxUsername).(*TextField).Value()),
			AuthType:  f.Field(calDAVIdxAuth).(*SelectField).Value(),
		}
		if msg.ServerURL == "" {
			f.SetError(calDAVIdxServer, "Server URL is required")
			return nil
		}
		if msg.Username == "" {
			f.SetError(calDAVIdxUsername, "Username is required")
			return nil
		}
		if calendarAuthIsOAuth(msg.AuthType) {
			msg.OAuthClientID = strings.TrimSpace(f.Field(calDAVIdxOAuthClientID).(*TextField).Value())
			msg.OAuthClientSecret = strings.TrimSpace(f.Field(calDAVIdxOAuthClientSecret).(*TextField).Value())
			msg.AllowInsecure = f.Field(calDAVIdxOAuthAllowInsecure).(*CheckboxField).Checked()
			if msg.OAuthClientID == "" {
				f.SetError(calDAVIdxOAuthClientID, "Client ID is required")
				return nil
			}
			if msg.OAuthClientSecret == "" {
				f.SetError(calDAVIdxOAuthClientSecret, "Client secret is required")
				return nil
			}
		} else {
			msg.Secret = f.Field(calDAVIdxSecret).(*TextField).Value()
			msg.AllowInsecure = f.Field(calDAVIdxAllowInsecure).(*CheckboxField).Checked()
			if strings.TrimSpace(msg.Secret) == "" {
				f.SetError(calDAVIdxSecret, "Credential is required")
				return nil
			}
		}
		return func() tea.Msg { return msg }
	})
	return form
}

// syncEnabled reports whether the Sync checkbox is currently on. Returns
// false in linked mode, where the checkbox does not exist.
func syncEnabled(f *Form) bool {
	if f.ItemCount() <= cdIdxSync {
		return false
	}
	cb, ok := f.Field(cdIdxSync).(*CheckboxField)
	return ok && cb.Checked()
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

// remoteAccountSummary compacts username + auth type into one value, e.g.
// "alice@example.com (oauth2)". Empty when neither is known.
func remoteAccountSummary(params CalendarDialogParams) string {
	switch {
	case params.RemoteUsername != "" && params.RemoteAuthType != "":
		return params.RemoteUsername + " (" + params.RemoteAuthType + ")"
	case params.RemoteUsername != "":
		return params.RemoteUsername
	case params.RemoteAuthType != "":
		return params.RemoteAuthType
	}
	return ""
}

// syncHealthLine is one row of the linked dialog's sync summary: raw text
// plus the style to apply after width truncation. Styling happens at render
// time (inside the StaticField's styleFn) so the text can be truncated to
// the dialog's content width without slicing through ANSI escapes.
type syncHealthLine struct {
	text  string
	style lipgloss.Style
}

// syncHealthDialogLines renders the calendar's sync health for the dialog: a
// loud error line plus an actionable re-link hint when the last sync failed, or
// a quiet "Last synced" line otherwise. Returns nil for unlinked calendars or
// linked-but-never-attempted ones (nothing useful to say yet).
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
// translating; everything else falls back to the first line of the raw error.
func humanizeSyncError(raw string) string {
	if strings.Contains(raw, "invalid_grant") {
		// Short on purpose: the hint line right below carries the action
		// ("Press Re-authenticate below to fix."), and the dialog line
		// must fit ~56 cols after the "⚠ Sync failed: " prefix.
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

func (m CalendarDialogModel) SetSize(w, h int) CalendarDialogModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	if m.discoveryPicker != nil {
		picker := m.discoveryPicker.SetSize(w, h)
		m.discoveryPicker = &picker
		return m
	}
	m.form.SetWidth(m.dialog.ContentWidth())
	if m.contentWidth != nil {
		*m.contentWidth = m.dialog.ContentWidth()
	}
	m.syncBodyViewport(true)
	return m
}

func (m CalendarDialogModel) formViewportHeight() int {
	const chromeLines = 2 + // top + bottom border
		1 + // top padding (PaddingY)
		2 + // dialog title + blank line
		2 // blank line + help footer
	extra := 0
	if m.testStatus != "" {
		extra = 1
	}
	actionLines := 1 + max(lipgloss.Height(m.form.ButtonRowView()), 1) // separator + buttons
	return max(m.dialog.height-chromeLines-actionLines-extra, 1)
}

func (m *CalendarDialogModel) syncBodyViewport(keepFocusVisible bool) {
	cw := m.dialog.ContentWidth()
	if cw <= 0 || m.dialog.height <= 0 {
		return
	}
	bodyLines := strings.Split(m.form.BodyView(), "\n")
	m.body.SetWidth(cw)
	m.body.SetHeight(min(len(bodyLines), m.formViewportHeight()))
	m.body.SetContentLines(bodyLines)
	if keepFocusVisible {
		m.keepFocusedFieldVisible()
	}
}

func (m *CalendarDialogModel) keepFocusedFieldVisible() {
	if m.body.Height() <= 0 {
		return
	}
	line := m.form.FocusedLine()
	if line < 0 {
		// Focus is on the button row, not a body field; leave the
		// scroll position where the last field left it.
		return
	}
	if line < m.body.YOffset() {
		m.body.ScrollUp(m.body.YOffset() - line)
		return
	}
	bottom := m.body.YOffset() + m.body.Height() - 1
	if line > bottom {
		m.body.ScrollDown(line - bottom)
	}
}

func (m CalendarDialogModel) bodyOverflows() bool {
	return m.body.TotalLineCount() > m.body.VisibleLineCount()
}

func (m CalendarDialogModel) scrollHint() string {
	if !m.bodyOverflows() {
		return ""
	}
	switch {
	case m.body.AtTop():
		return "↓ more"
	case m.body.AtBottom():
		return "↑ more"
	default:
		return "↑↓ more"
	}
}

func (m CalendarDialogModel) actionsSeparator(w int) string {
	faint := lipgloss.NewStyle().Faint(true)
	hint := m.scrollHint()
	hw := lipgloss.Width(hint)
	if hint == "" || w <= hw+2 {
		return faint.Render(strings.Repeat("─", w))
	}
	left := (w - hw - 2) / 2
	right := w - hw - 2 - left
	return faint.Render(strings.Repeat("─", left)) + " " + faint.Render(hint) + " " + faint.Render(strings.Repeat("─", right))
}

func (m CalendarDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m CalendarDialogModel) Update(msg tea.Msg) (CalendarDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	if m.discoveryPicker != nil {
		picker, cmd := m.discoveryPicker.Update(msg)
		m.discoveryPicker = &picker
		return m, cmd
	}

	if _, ok := msg.(calendarConnectionBackMsg); ok {
		if m.localDraft == nil {
			return m, nil
		}
		restored := NewCalendarDialogModel(*m.localDraft, m.theme).
			SetSize(m.dialog.width, m.dialog.height)
		return restored, nil
	}

	if _, ok := msg.(testConnectionPressedMsg); ok {
		m, cmd := m.handleTestPressed()
		m.syncBodyViewport(true)
		return m, cmd
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
		m.syncBodyViewport(true)
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
	if mw, ok := msg.(tea.MouseWheelMsg); ok {
		var cmd tea.Cmd
		m.syncBodyViewport(false)
		m.body, cmd = m.body.Update(mw)
		return m, cmd
	}

	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	m.syncBodyViewport(true)
	return m, cmd
}

func (m CalendarDialogModel) View() string {
	if m.discoveryPicker != nil {
		return m.discoveryPicker.View()
	}
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))
	m.syncBodyViewport(false)
	cw := m.dialog.ContentWidth()
	parts := []string{m.body.View()}
	if m.testStatus != "" {
		parts = append(parts, truncateTo(m.testStatus, cw))
	}
	parts = append(parts, m.actionsSeparator(cw), m.form.ButtonRowView())
	body := strings.Join(parts, "\n")
	content := mouseSweep(m.dialog.Box(body))
	return content
}

// handleTestPressed validates the remote fields and, when they're
// populated, emits a CalendarTestRequestedMsg so the parent can run the
// authenticated ping. Errors show inline without contacting the server.
func (m CalendarDialogModel) handleTestPressed() (CalendarDialogModel, tea.Cmd) {
	if m.connectionMode == nil || !*m.connectionMode {
		return m, nil
	}
	// The oauth2 layout has no password to ping with — there is no token
	// until the browser flow runs, which happens on discovery.
	if calendarAuthIsOAuth(m.form.Field(calDAVIdxAuth).(*SelectField).Value()) {
		m.testStatus = lipgloss.NewStyle().Foreground(m.theme.TextDim).Italic(true).
			Render("Test runs after Google authorization — discover to connect")
		return m, nil
	}
	url := strings.TrimSpace(m.form.Field(calDAVIdxServer).(*TextField).Value())
	user := strings.TrimSpace(m.form.Field(calDAVIdxUsername).(*TextField).Value())
	auth := m.form.Field(calDAVIdxAuth).(*SelectField).Value()
	pass := m.form.Field(calDAVIdxSecret).(*TextField).Value()
	ins := m.form.Field(calDAVIdxAllowInsecure).(*CheckboxField).Checked()

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
