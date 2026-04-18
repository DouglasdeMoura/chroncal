package tui

import (
	"image/color"
	"strings"

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
}

// CalendarSavedMsg is emitted when the user saves the dialog. ID == 0 means
// "create a new calendar"; otherwise it's an update.
type CalendarSavedMsg struct {
	ID          int64
	Name        string
	Color       string
	Description string
	OwnerEmail  string

	// Remote connection — only meaningful when RemoteURL is non-empty and
	// the calendar is not already linked. OAuth flows are handled via the
	// CLI, not the dialog.
	RemoteURL     string
	Username      string
	AuthType      string // "basic" | "bearer"
	Password      string // populated only when AuthType == "basic"
	AllowInsecure bool
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

// CalendarDialogClosedMsg is emitted when the user cancels the dialog.
type CalendarDialogClosedMsg struct{}

// paletteSwatches is the preset color grid shown in the calendar dialog.
var paletteSwatches = []string{
	"#0074D9", "#7FDBFF", "#39CCCC", "#B10DC9",
	"#F012BE", "#85144b", "#FF4136", "#FF851B",
	"#FFDC00", "#3D9970", "#2ECC40", "#01FF70",
	"#111111", "#AAAAAA",
}

// Form field indices. Local fields are always present. Index 4 is an empty
// spacer row; index 5 is the Sync toggle in unlinked mode or a read-only
// status line in linked mode. Remote fields (6..10) exist only when Sync
// is on.
const (
	cdIdxName        = 0
	cdIdxColor       = 1
	cdIdxDescription = 2
	cdIdxEmail       = 3
	// Index 4 is an empty spacer StaticField.
	cdIdxSync = 5

	// Present only when Sync is on (unlinked mode only).
	cdIdxRemoteURL     = 6
	cdIdxUsername      = 7
	cdIdxAuth          = 8
	cdIdxPassword      = 9
	cdIdxAllowInsecure = 10
)

var authOptions = []SelectOption{
	{Label: "Basic", Value: "basic"},
	{Label: "Bearer", Value: "bearer"},
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
	accentColor  color.Color
	mutedColor   color.Color
	textDimColor color.Color
}

// NewCalendarDialogModel builds a dialog for create (params.ID==0) or edit.
func NewCalendarDialogModel(params CalendarDialogParams, theme Theme) CalendarDialogModel {
	title := "New calendar"
	if params.ID > 0 {
		title = "Edit calendar"
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

	placeholderStyle := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)

	nameField := NewTextField("e.g. Work")
	nameField.SetValue(params.Name)
	nameField.SetCharLimit(256)
	nameField.SetPlaceholderStyle(placeholderStyle)

	colorField := NewColorField(paletteSwatches, params.Color, theme.Selected, theme.Muted, theme.TextDim)

	descField := NewTextField("Shared family schedule")
	descField.SetValue(params.Description)
	descField.SetCharLimit(500)
	descField.SetPlaceholderStyle(placeholderStyle)

	emailField := NewTextField("you@example.com")
	emailField.SetValue(params.OwnerEmail)
	emailField.SetCharLimit(256)
	emailField.SetPlaceholderStyle(placeholderStyle)

	items := []FormItem{
		{Label: "Name", Field: nameField, Required: true},
		{Label: "Color", Field: colorField, Required: true},
		{Label: "Description", Field: descField},
		{Label: "Owner email", Field: emailField},
		{Label: "", Field: NewStaticField("", nil)},
	}

	if params.RemoteLinked {
		summary := remoteStatusLine(params, theme)
		items = append(items, FormItem{
			Label: "",
			Field: NewStaticField(summary, nil),
		})
	} else {
		sync := NewCheckboxField("", false)
		sync.SetContent("Enable CalDAV sync")
		items = append(items, FormItem{Label: "Sync", Field: sync})
	}

	form := NewForm("Save", formStyles, items...)

	savedID := params.ID
	linked := params.RemoteLinked
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
		}

		if !linked && syncEnabled(f) {
			urlVal := strings.TrimSpace(f.Field(cdIdxRemoteURL).(*TextField).Value())
			userVal := strings.TrimSpace(f.Field(cdIdxUsername).(*TextField).Value())
			authVal := f.Field(cdIdxAuth).(*SelectField).Value()
			passVal := f.Field(cdIdxPassword).(*TextField).Value()
			allowIns := f.Field(cdIdxAllowInsecure).(*CheckboxField).Checked()

			if urlVal == "" {
				f.SetError(cdIdxRemoteURL, "Remote URL is required when Sync is on")
				return nil
			}
			if userVal == "" {
				f.SetError(cdIdxUsername, "Username is required when Sync is on")
				return nil
			}
			if passVal == "" {
				if authVal == "bearer" {
					f.SetError(cdIdxPassword, "Access token is required for bearer auth")
				} else {
					f.SetError(cdIdxPassword, "Password is required for basic auth")
				}
				return nil
			}

			msg.RemoteURL = urlVal
			msg.Username = userVal
			msg.AuthType = authVal
			msg.Password = passVal
			msg.AllowInsecure = allowIns
		}

		return func() tea.Msg { return msg }
	})

	form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return CalendarDialogClosedMsg{} }
	})

	m := CalendarDialogModel{
		id:           params.ID,
		name:         params.Name,
		linked:       params.RemoteLinked,
		dialog:       dialog,
		form:         form,
		help:         newThemedHelp(theme),
		accentColor:  theme.Selected,
		mutedColor:   theme.Muted,
		textDimColor: theme.TextDim,
	}

	if params.RemoteLinked {
		id := params.ID
		name := params.Name
		form.SetLeadingActionButton("Disconnect", ButtonDanger, func() tea.Msg {
			return CalendarDisconnectRemoteRequestedMsg{ID: id, Name: name}
		})
	}

	syncTheme := theme
	syncPlaceholderStyle := placeholderStyle
	form.OnRebuild(func(f *Form) {
		// Toggle remote fields on/off in lockstep with the Sync checkbox.
		if linked {
			return
		}
		syncOn := syncEnabled(f)
		hasRemote := f.ItemCount() > cdIdxSync+1
		switch {
		case syncOn && !hasRemote:
			insecure := NewCheckboxField("", false)
			insecure.SetContent("allow plain HTTP")
			f.AppendItems(
				FormItem{Label: "Remote URL", Field: newRemoteURLField("", syncPlaceholderStyle), Required: true},
				FormItem{Label: "Username", Field: newUsernameField("", syncPlaceholderStyle), Required: true},
				FormItem{Label: "Auth", Field: newAuthField("")},
				FormItem{Label: "Password", Field: newPasswordField(syncPlaceholderStyle), Required: true},
				FormItem{Label: "HTTP", Field: insecure},
			)
		case !syncOn && hasRemote:
			f.RemoveItems(cdIdxSync + 1)
			f.ClearError()
		}

		// Keep the Password row's label and placeholder in sync with the
		// selected auth type: basic -> password, bearer -> access token.
		if syncOn && f.ItemCount() > cdIdxPassword {
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
		if syncOn && f.ItemCount() > cdIdxAllowInsecure {
			urlVal := strings.TrimSpace(f.Field(cdIdxRemoteURL).(*TextField).Value())
			insecure := f.Field(cdIdxAllowInsecure).(*CheckboxField)
			wasAuto := insecure.AutoChecked()
			if isLocalhostHTTP(urlVal) {
				insecure.SetChecked(true)
				insecure.SetAutoChecked(true)
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
			}
		}
	})
	m.form = form

	return m
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

func newRemoteURLField(value string, placeholderStyle lipgloss.Style) *TextField {
	f := NewTextField("https://cal.example.com/dav/calendars/work/")
	f.SetValue(value)
	f.SetCharLimit(512)
	f.SetPlaceholderStyle(placeholderStyle)
	return f
}

func newUsernameField(value string, placeholderStyle lipgloss.Style) *TextField {
	f := NewTextField("you@example.com")
	f.SetValue(value)
	f.SetCharLimit(256)
	f.SetPlaceholderStyle(placeholderStyle)
	return f
}

func newAuthField(authType string) *SelectField {
	f := NewSelectField(authOptions)
	f.SetSelected(authOptionIndex(authType))
	return f
}

func newPasswordField(placeholderStyle lipgloss.Style) *TextField {
	f := NewTextField("your password")
	f.SetCharLimit(256)
	f.SetEchoPassword(true)
	f.SetPlaceholderStyle(placeholderStyle)
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
	content := mouseSweep(m.dialog.Box(m.form.View()))
	return content
}
