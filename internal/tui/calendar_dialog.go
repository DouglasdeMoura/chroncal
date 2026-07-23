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
	// exists, since the first calendar is auto-promoted by the service
	// (the checkbox would be meaningless and noisy in that case).
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
// dialog. The parent is responsible for showing the confirm dialog.
type CalendarDeleteRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarExportRequestedMsg is a neutral request to export the calendar. The
// parent owns the file I/O; this message only identifies the target by its
// immutable ID so the host can resolve fresh data at export time.
type CalendarExportRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarSetDefaultRequestedMsg asks the app to promote a calendar to default.
type CalendarSetDefaultRequestedMsg struct {
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

// Form field indices for the calendar metadata fields. Index 4 is an empty
// spacer; in edit mode index 5 is the Display Calendar checkbox and index 6+
// holds the Location/Account row and (for remote calendars) sync-health
// lines. Those later indices are dynamic, so only the metadata fields below
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

// NewCalendarDialogModel builds a dialog for create (params.ID==0) or edit.
func NewCalendarDialogModel(params CalendarDialogParams, theme Theme) CalendarDialogModel {
	title := "New local calendar"
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

	// The dialog width isn't known until SetSize. Truncate compact account
	// context at render time so long names and errors stay on one row.
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
	// Edit mode surfaces the calendar's visibility and owning context as
	// actionable rows: a Display Calendar checkbox (visibility is immediate
	// and never auto-saved with metadata) and either "Location: Local" or an
	// "Account: <name> ›" opener that drills into the owning account. Create
	// mode has no immutable ID, so neither row applies there.
	var (
		visibilityCb  *CheckboxField
		accountOpener *OpenerField
		openerIdx     = -1
	)
	if params.ID > 0 {
		visibility := NewCheckboxField("", !params.Hidden)
		visibility.SetContent("Display calendar")
		visibilityCb = visibility
		items = append(items, FormItem{Label: "", Field: visibility, AlignToFieldColumn: true})

		// Account and Location sit in the shared label column like every
		// other row (Apple Settings detail-row layout); the opener's value
		// carries the drill-in chevron.
		if params.RemoteLinked {
			accountName := strings.TrimSpace(params.AccountName)
			if accountName == "" {
				accountName = "Connected account"
			}
			openerIdx = len(items)
			opener := NewOpenerField(accountName + " ›")
			accountOpener = opener
			items = append(items, FormItem{Label: "Account", Field: opener})
			for _, line := range syncHealthDialogLines(params, theme) {
				items = append(items, staticLine(line.text, line.style))
			}
			// Account calendars carry no Delete button (deleting here would
			// only remove the local copy, not the account's calendar); this
			// footnote explains why and points at the local alternative.
			note := lipgloss.NewStyle().Foreground(theme.TextDim)
			ownership := "This calendar lives in your " + accountName + " account."
			if strings.TrimSpace(params.AccountName) == "" {
				ownership = "This calendar lives in your connected account."
			}
			items = append(items,
				FormItem{Label: "", Field: NewStaticField("", nil)},
				staticLine(ownership, note),
				staticLine("Turn off Display calendar to hide it on this device.", note),
			)
		} else {
			items = append(items, FormItem{Label: "Location", Field: NewStaticField("Local", nil)})
		}
	}

	form := NewForm("Save", formStyles, items...)

	savedID := params.ID
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

	localDraft := params

	m := CalendarDialogModel{
		id:                params.ID,
		name:              params.Name,
		linked:            params.RemoteLinked,
		dialog:            dialog,
		form:              form,
		body:              viewport.New(),
		help:              newThemedHelp(theme),
		theme:             theme,
		accentColor:       theme.Selected,
		mutedColor:        theme.Muted,
		textDimColor:      theme.TextDim,
		saveMakeDefault:   saveMakeDefault,
		accountConnection: false,
		localDraft:        &localDraft,
		contentWidth:      contentWidth,
		hidden:            params.Hidden,
		visibilityCb:      visibilityCb,
		accountOpener:     accountOpener,
	}
	m.body.MouseWheelEnabled = true

	// Edit mode, not yet default: surface "Set as Default" without forcing a
	// trip through the manage-calendars list.
	if params.ID > 0 && !params.IsDefault {
		id := params.ID
		name := params.Name
		form.SetLeadingActionButton("Set as Default", Button, func() tea.Msg {
			return CalendarSetDefaultRequestedMsg{ID: id, Name: name}
		})
	}

	// Remote calendars drill into their owning account via the inline
	// "Account: <name> ›" opener rather than a separate button, so the
	// opener's Enter emits the canonical AccountSettingsRequestedMsg the
	// host already routes (and the calendar manager intercepts to push the
	// account detail without disturbing the in-progress calendar draft).
	if openerIdx >= 0 {
		accountID := params.AccountID
		capturedIdx := openerIdx
		form.OnFieldEnter(func(f *Form, field int) tea.Cmd {
			if field != capturedIdx {
				return nil
			}
			return func() tea.Msg { return AccountSettingsRequestedMsg{AccountID: accountID} }
		})
	}

	// Edit mode exposes Delete as a leading action targeting the calendar's
	// immutable ID; it is destructive (ButtonDanger) and only requests
	// removal — the host owns the safe-confirm flow. Account calendars get
	// no Delete: the button could only drop the local copy while the
	// account still owns the calendar, so the form explains that in a
	// footnote instead (hide locally via Display calendar; manage
	// membership in Account ▸ Manage Calendars). Export is a manager-only
	// affordance (the legacy app has no export handler yet), so it is gated
	// on ManagerEmbedded to avoid a no-op button in legacy-wired dialogs.
	if params.ID > 0 {
		id := params.ID
		name := params.Name
		if params.ManagerEmbedded {
			form.SetLeadingActionButton("Export Calendar…", Button, func() tea.Msg {
				return CalendarExportRequestedMsg{ID: id, Name: name}
			})
		}
		if !params.RemoteLinked {
			form.SetLeadingActionButton("Delete Calendar…", ButtonDanger, func() tea.Msg {
				return CalendarDeleteRequestedMsg{ID: id, Name: name}
			})
		}
		form.SetSeparateLeadingActions(true)
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

	m.form = form
	return m
}

// NewAccountDialogModel opens account sign-in directly. Remote collection
// discovery is an account concern; New Local Calendar remains a local-only flow.
func NewAccountDialogModel(theme Theme) CalendarDialogModel {
	dialog := NewDialog("Add Account", DefaultDialogStyles())
	dialog.SetWidth(62)
	form := newCalDAVConnectionForm(theme, "")
	m := CalendarDialogModel{
		dialog:            dialog,
		form:              form,
		body:              viewport.New(),
		help:              newThemedHelp(theme),
		theme:             theme,
		accentColor:       theme.Selected,
		mutedColor:        theme.Muted,
		textDimColor:      theme.TextDim,
		accountConnection: true,
	}
	m.body.MouseWheelEnabled = true
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
	form := NewForm("Sign In", styles,
		FormItem{Label: "Server URL", Field: newRemoteURLField(""), Required: true},
		FormItem{Label: "Username", Field: newUsernameField(usernamePrefill), Required: true},
		FormItem{Label: "Auth", Field: newAuthField("basic"), Required: true},
		FormItem{Label: "Password", Field: newPasswordField(), Required: true},
		FormItem{Label: "HTTP", Field: insecure},
	)
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
// plus the style to apply after width truncation. Styling happens at render
// time (inside the StaticField's styleFn) so the text can be truncated to
// the dialog's content width without slicing through ANSI escapes.
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
// translating; everything else falls back to the first line of the raw error.
func humanizeSyncError(raw string) string {
	if strings.Contains(raw, "invalid_grant") {
		// Short on purpose: the hint line right below carries the action,
		// and the dialog line must fit ~56 cols after the
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

// SetAccountName refreshes account context without rebuilding the form, so
// in-progress calendar metadata edits survive an account rename. It updates
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

// Draft returns the calendar's current editable state as params: the live
// field values plus the original context (ID, account linkage, sync health)
// and the current visibility. Hosts use it to preserve an unsaved calendar
// draft across a drill into the owning account.
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
// Calendar checkbox without emitting a toggle message.
func (m CalendarDialogModel) SetHidden(h bool) CalendarDialogModel {
	m.hidden = h
	if m.visibilityCb != nil {
		m.visibilityCb.SetChecked(!h)
	}
	return m
}

// leftMovesCursor reports whether the Left arrow would edit the focused field
// (a text or color input) rather than navigate, so the calendar manager can
// avoid stealing Left as a Back gesture while the user is editing. Buttons
// and non-editing fields (checkbox, opener) leave Left free to pop.
// dirtyMetadata reports whether any editable metadata field differs from the
// values the detail opened with, i.e. whether an unsaved draft exists. The
// Display checkbox is excluded: visibility commits immediately and is never
// part of the draft. Hosts use this to keep navigation gestures from
// silently discarding typed edits.
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

// SetInspectorSize prepares the existing form for borderless rendering inside
// the Calendars manager. Rendering stays pure: body content and viewport
// dimensions are refreshed here and after Update, never from InspectorView.
// Blur returns a copy whose form holds no keyboard focus, so the manager can
// render it as the root selection preview while the source list owns input.
func (m CalendarDialogModel) Blur() CalendarDialogModel {
	m.form = m.form.Blur()
	return m
}

func (m CalendarDialogModel) SetInspectorSize(w, h int) CalendarDialogModel {
	w = max(w, 1)
	h = max(h, 1)
	m.form.SetWidth(w)
	if m.contentWidth != nil {
		*m.contentWidth = w
	}
	bodyLines := strings.Split(m.form.BodyView(), "\n")
	statusLines := 0
	if m.testStatus != "" {
		statusLines = 1
	}
	buttonLines := max(lipgloss.Height(m.form.ButtonRowView()), 1)
	bodyHeight := max(h-2-statusLines-1-buttonLines, 1)
	m.body.SetWidth(w)
	m.body.SetHeight(min(len(bodyLines), bodyHeight))
	m.body.SetContentLines(bodyLines)
	m.keepFocusedFieldVisible()
	if m.discoveryPicker != nil {
		picker := m.discoveryPicker.SetSize(w, h)
		m.discoveryPicker = &picker
	}
	return m
}

// InspectorView renders the calendar/add-account form without another dialog
// border so the manager's grouped hierarchy remains mounted beside it.
func (m CalendarDialogModel) InspectorView(w, h int) string {
	if m.discoveryPicker != nil {
		return padLines(strings.Split(m.discoveryPicker.View(), "\n"), w, h)
	}
	parts := []string{lipgloss.NewStyle().Bold(true).Render(truncateTo(m.dialog.title, w)), m.body.View()}
	if m.testStatus != "" {
		parts = append(parts, truncateTo(m.testStatus, w))
	}
	parts = append(parts, m.actionsSeparator(w), m.form.ButtonRowView())
	return padLines(strings.Split(strings.Join(parts, "\n"), "\n"), w, h)
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
			// A click on the Display Calendar checkbox toggles it inside the
			// form; compare pre/post state so the mouse path emits the same
			// CalendarVisibilityToggledMsg as the keyboard path.
			preVisible := m.visibilityChecked()
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(MouseEvent{IsClick: true, Target: target})
			m.syncBodyViewport(true)
			return m.applyVisibilityToggle(preVisible, cmd)
		}
		return m, nil
	}
	if mw, ok := msg.(tea.MouseWheelMsg); ok {
		var cmd tea.Cmd
		m.syncBodyViewport(false)
		m.body, cmd = m.body.Update(mw)
		return m, cmd
	}

	preVisible := m.visibilityChecked()
	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	m.syncBodyViewport(true)
	return m.applyVisibilityToggle(preVisible, cmd)
}

// visibilityChecked reports the Display Calendar checkbox's current state, or
// true when there is no checkbox (create mode) so a no-op comparison never
// reports a spurious change.
func (m CalendarDialogModel) visibilityChecked() bool {
	if m.visibilityCb == nil {
		return true
	}
	return m.visibilityCb.Checked()
}

// applyVisibilityToggle compares the checkbox state before and after a form
// update; a change emits CalendarVisibilityToggledMsg with the DESIRED hidden
// state, mirrors it into the local model so the dot flips without a reload,
// and batches with any cmd the form update produced. Metadata Save/Cancel
// never persists visibility.
func (m CalendarDialogModel) applyVisibilityToggle(preVisible bool, cmd tea.Cmd) (CalendarDialogModel, tea.Cmd) {
	if m.visibilityCb == nil {
		return m, cmd
	}
	postVisible := m.visibilityCb.Checked()
	if postVisible == preVisible {
		return m, cmd
	}
	m.hidden = !postVisible
	id := m.id
	toggle := func() tea.Msg { return CalendarVisibilityToggledMsg{ID: id, Hidden: !postVisible} }
	if cmd == nil {
		return m, toggle
	}
	return m, tea.Batch(cmd, toggle)
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
	if !m.accountConnection {
		return m, nil
	}
	// The oauth2 layout has no password to ping with — there is no token
	// until the browser flow runs, which happens on sign-in.
	if calendarAuthIsOAuth(m.form.Field(calDAVIdxAuth).(*SelectField).Value()) {
		m.testStatus = lipgloss.NewStyle().Foreground(m.theme.TextDim).Italic(true).
			Render("Connection test runs after Google authorization — sign in to continue")
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
