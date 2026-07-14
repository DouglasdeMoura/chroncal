package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

// AccountDialogRequestedMsg opens the account connection form.
type AccountDialogRequestedMsg struct{}

// AccountDialogClosedMsg closes the account connection form without changes.
type AccountDialogClosedMsg struct{}

// AccountConnectRequestedMsg carries one account's non-secret settings and
// whichever credential material applies to its authentication type.
type AccountConnectRequestedMsg struct {
	Name              string
	ServerURL         string
	Username          string
	AuthType          string
	Secret            string
	OAuthClientID     string
	OAuthClientSecret string
	AllowInsecure     bool
}

const (
	accountIdxName = iota
	accountIdxServer
	accountIdxUsername
	accountIdxAuth
	accountIdxSecret
	accountIdxAllowInsecure
)

const (
	accountIdxOAuthClientID      = accountIdxSecret
	accountIdxOAuthClientSecret  = accountIdxAllowInsecure
	accountIdxOAuthAllowInsecure = accountIdxAllowInsecure + 1
)

// AccountDialogModel collects account credentials before discovery. Its auth
// tail swaps between password/token and OAuth client configuration without
// discarding values when the user switches back and forth.
type AccountDialogModel struct {
	dialog Dialog
	form   Form
	help   help.Model
}

func NewAccountDialogModel(theme Theme) AccountDialogModel {
	dialog := NewDialog("Add CalDAV account", DefaultDialogStyles())
	dialog.SetWidth(66)

	styles := DefaultFormStyles()
	styles.LabelLayout = LabelInline
	styles.ShowFocusMarker = true
	styles.ButtonAlign = ButtonAlignRight
	styles.ButtonRule = true

	name := NewTextField("e.g. Google · Personal")
	name.SetCharLimit(256)
	server := NewTextField("https://example.com/.well-known/caldav")
	server.SetCharLimit(512)
	username := newUsernameField("")
	authField := newAuthField("basic")

	items := []FormItem{
		{Label: "Name", Field: name, Required: true},
		{Label: "Server URL", Field: server, Required: true},
		{Label: "Username", Field: username, Required: true},
		{Label: "Auth", Field: authField, Required: true},
		{Label: "Password", Field: newPasswordField(), Required: true},
	}
	insecure := NewCheckboxField("", false)
	insecure.SetContent("allow plain HTTP")
	items = append(items, FormItem{Label: "HTTP", Field: insecure})

	form := NewForm("Discover calendars", styles, items...)
	form.OnCancel(func(*Form) tea.Cmd {
		return func() tea.Msg { return AccountDialogClosedMsg{} }
	})

	var snapshot struct {
		secret, clientID, clientSecret string
		allowInsecure                  bool
	}
	oauthLayout := new(bool)

	snapshotTail := func(f *Form) {
		if *oauthLayout {
			snapshot.clientID = f.Field(accountIdxOAuthClientID).(*TextField).Value()
			snapshot.clientSecret = f.Field(accountIdxOAuthClientSecret).(*TextField).Value()
			snapshot.allowInsecure = f.Field(accountIdxOAuthAllowInsecure).(*CheckboxField).Checked()
			return
		}
		snapshot.secret = f.Field(accountIdxSecret).(*TextField).Value()
		snapshot.allowInsecure = f.Field(accountIdxAllowInsecure).(*CheckboxField).Checked()
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
		authType := f.Field(accountIdxAuth).(*SelectField).Value()
		if calendarAuthIsOAuth(authType) != *oauthLayout {
			snapshotTail(f)
			f.RemoveItems(accountIdxSecret)
			f.ClearError()
			appendTail(f, authType)
		}
		if !*oauthLayout {
			secret := f.Field(accountIdxSecret).(*TextField)
			if authType == "bearer" {
				f.SetItemLabel(accountIdxSecret, "Token")
				secret.SetPlaceholder("paste your API token")
			} else {
				f.SetItemLabel(accountIdxSecret, "Password")
				secret.SetPlaceholder("your password")
			}
		}
	})

	form.OnSubmit(func(f *Form) tea.Cmd {
		msg := AccountConnectRequestedMsg{
			Name:      strings.TrimSpace(f.Field(accountIdxName).(*TextField).Value()),
			ServerURL: strings.TrimSpace(f.Field(accountIdxServer).(*TextField).Value()),
			Username:  strings.TrimSpace(f.Field(accountIdxUsername).(*TextField).Value()),
			AuthType:  f.Field(accountIdxAuth).(*SelectField).Value(),
		}
		if msg.Name == "" {
			f.SetError(accountIdxName, "Account name is required")
			return nil
		}
		if msg.ServerURL == "" {
			f.SetError(accountIdxServer, "Server URL is required")
			return nil
		}
		if msg.Username == "" {
			f.SetError(accountIdxUsername, "Username is required")
			return nil
		}
		if calendarAuthIsOAuth(msg.AuthType) {
			msg.OAuthClientID = strings.TrimSpace(f.Field(accountIdxOAuthClientID).(*TextField).Value())
			msg.OAuthClientSecret = strings.TrimSpace(f.Field(accountIdxOAuthClientSecret).(*TextField).Value())
			msg.AllowInsecure = f.Field(accountIdxOAuthAllowInsecure).(*CheckboxField).Checked()
			if msg.OAuthClientID == "" {
				f.SetError(accountIdxOAuthClientID, "Client ID is required")
				return nil
			}
			if msg.OAuthClientSecret == "" {
				f.SetError(accountIdxOAuthClientSecret, "Client secret is required")
				return nil
			}
		} else {
			msg.Secret = f.Field(accountIdxSecret).(*TextField).Value()
			msg.AllowInsecure = f.Field(accountIdxAllowInsecure).(*CheckboxField).Checked()
			if strings.TrimSpace(msg.Secret) == "" {
				f.SetError(accountIdxSecret, "Credential is required")
				return nil
			}
		}
		return func() tea.Msg { return msg }
	})

	return AccountDialogModel{dialog: dialog, form: form, help: newThemedHelp(theme)}
}

func (m AccountDialogModel) SetSize(w, h int) AccountDialogModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.dialog.SetWidth(min(w, 66))
	m.form.SetWidth(m.dialog.ContentWidth())
	return m
}

func (m AccountDialogModel) BoxSize() (int, int) { return lipgloss.Size(m.View()) }

func (m AccountDialogModel) Update(msg tea.Msg) (AccountDialogModel, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(size.Width, size.Height), nil
	}
	if click, ok := msg.(tea.MouseClickMsg); ok {
		if click.Button != tea.MouseLeft {
			return m, nil
		}
		bw, bh := m.BoxSize()
		ox := (m.dialog.width - bw) / 2
		oy := (m.dialog.height - bh) / 2
		var cmd tea.Cmd
		m.form, cmd = m.form.Update(MouseEvent{IsClick: true, Target: mouseResolve(click.X-ox, click.Y-oy)})
		return m, cmd
	}
	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m AccountDialogModel) View() string {
	keys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "activate")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
	m.dialog.SetFooter(m.help.ShortHelpView(keys))
	return mouseSweep(m.dialog.Box(m.form.View()))
}

// AccountCalendarsImportRequestedMsg applies the picker selection.
type AccountCalendarsImportRequestedMsg struct {
	AccountID int64
	Paths     []string
}

// AccountCalendarPickerClosedMsg closes the discovery picker without importing.
type AccountCalendarPickerClosedMsg struct{}

// AccountCalendarPickerModel presents every discovered collection, including
// read-only and unsupported rows, while only allowing usable event calendars
// to be selected for import.
type AccountCalendarPickerModel struct {
	discovery account.Discovery
	selected  map[string]bool
	shell     ListDialogModel
	theme     Theme
}

func NewAccountCalendarPickerModel(discovery account.Discovery, theme Theme) AccountCalendarPickerModel {
	selected := make(map[string]bool, len(discovery.Calendars))
	for _, remote := range discovery.Calendars {
		if remote.Importable {
			selected[remote.Path] = true
		}
	}
	m := AccountCalendarPickerModel{
		discovery: discovery,
		selected:  selected,
		shell: NewListDialogModel(newThemedHelp(theme)).
			SetTitle("Choose calendars · " + textsafe.Display(discovery.Account.DisplayName)).
			SetSelectedColor(theme.Selected),
		theme: theme,
	}
	return m.refresh()
}

func (m AccountCalendarPickerModel) SetSize(w, h int) AccountCalendarPickerModel {
	m.shell = m.shell.SetSize(w, h)
	return m.refresh()
}

func (m AccountCalendarPickerModel) BoxSize() (int, int) { return m.shell.BoxSize() }

func (m AccountCalendarPickerModel) toggleCurrent() AccountCalendarPickerModel {
	idx := m.shell.Selected()
	if idx < 0 || idx >= len(m.discovery.Calendars) {
		return m
	}
	remote := m.discovery.Calendars[idx]
	if !remote.Importable || remote.Imported {
		return m
	}
	m.selected[remote.Path] = !m.selected[remote.Path]
	return m.refresh()
}

func (m AccountCalendarPickerModel) toggleAll() AccountCalendarPickerModel {
	allSelected := true
	for _, remote := range m.discovery.Calendars {
		if remote.Importable && !remote.Imported {
			allSelected = allSelected && m.selected[remote.Path]
		}
	}
	for _, remote := range m.discovery.Calendars {
		if !remote.Importable || remote.Imported {
			continue
		}
		m.selected[remote.Path] = !allSelected
	}
	return m.refresh()
}

func (m AccountCalendarPickerModel) importSelected() tea.Cmd {
	paths := make([]string, 0, len(m.selected))
	for _, remote := range m.discovery.Calendars {
		if remote.Importable && m.selected[remote.Path] {
			paths = append(paths, remote.Path)
		}
	}
	accountID := m.discovery.Account.ID
	return func() tea.Msg { return AccountCalendarsImportRequestedMsg{AccountID: accountID, Paths: paths} }
}

func (m AccountCalendarPickerModel) Update(msg tea.Msg) (AccountCalendarPickerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.SetSize(msg.Width, msg.Height), nil
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("space"))):
			if m.shell.FocusZone() == ListZoneList {
				return m.toggleCurrent(), nil
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("a"))):
			if m.shell.FocusZone() == ListZoneList {
				return m.toggleAll(), nil
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			if m.shell.FocusZone() == ListZoneList {
				return m, m.importSelected()
			}
		}
		var cmd tea.Cmd
		var handled bool
		m.shell, cmd, handled = m.shell.HandleKey(msg, func() tea.Msg { return AccountCalendarPickerClosedMsg{} })
		if handled {
			return m.refresh(), cmd
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		if idx, ok := m.shell.RowAtPosition(msg.X, msg.Y); ok {
			m.shell = m.shell.SetFocusZone(ListZoneList).SetSelected(idx)
			return m.toggleCurrent(), nil
		}
		if idx, ok := m.shell.ActionAtPosition(msg.X, msg.Y); ok {
			m.shell = m.shell.FocusAction(idx)
			m = m.refresh()
			return m, m.shell.actions[idx].Msg
		}
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.shell, cmd = m.shell.HandleMouseWheel(msg)
		return m, cmd
	}
	return m, nil
}

func (m AccountCalendarPickerModel) View() string { return m.shell.View() }

func (m AccountCalendarPickerModel) refresh() AccountCalendarPickerModel {
	rows := make([]string, 0, len(m.discovery.Calendars))
	for _, remote := range m.discovery.Calendars {
		checkbox := Glyphs["checkbox.off"]
		if m.selected[remote.Path] {
			checkbox = Glyphs["checkbox.on"]
		}
		name := remote.Name
		if name == "" {
			name = remote.Path
		}
		tags := make([]string, 0, 2)
		switch {
		case remote.Imported:
			tags = append(tags, "imported")
		case !remote.Importable:
			tags = append(tags, "unsupported")
		}
		if remote.Access == caldav.CalendarAccessRead {
			tags = append(tags, "read-only")
		}
		row := checkbox + " "
		if len(tags) > 0 {
			row += lipgloss.NewStyle().Foreground(m.theme.Muted).Render("["+strings.Join(tags, ", ")+"]") + " "
		}
		row += textsafe.Display(name)
		rows = append(rows, row)
	}
	m.shell = m.shell.SetRows(rows)

	if len(m.discovery.Calendars) == 0 {
		m.shell = m.shell.SetEmptyList("No calendar collections found.", []string{"The server returned no CalDAV calendar collections."})
		m.shell = m.shell.SetDetailTitle("").SetDetailLines(nil)
	} else {
		idx := m.shell.Selected()
		remote := m.discovery.Calendars[idx]
		name := remote.Name
		if name == "" {
			name = remote.Path
		}
		access := string(remote.Access)
		if access == "" {
			access = "unknown"
		}
		components := strings.Join(remote.SupportedComponentSet, ", ")
		if components == "" {
			components = "not advertised"
		}
		details := []string{
			"URL: " + textsafe.Display(remote.Path),
			"Access: " + access,
			"Components: " + components,
		}
		if remote.Description != "" {
			details = append(details, "", textsafe.Display(remote.Description))
		}
		m.shell = m.shell.SetDetailTitle(textsafe.Display(name)).SetDetailLines(details)
	}

	count := 0
	for _, remote := range m.discovery.Calendars {
		if remote.Importable && m.selected[remote.Path] {
			count++
		}
	}
	label := "Import"
	if count > 0 {
		label = fmt.Sprintf("Import (%d)", count)
	}
	m.shell = m.shell.SetActions([]ListDialogAction{
		{Label: label, Primary: true, Msg: m.importSelected()},
		{Label: "Cancel", Msg: func() tea.Msg { return AccountCalendarPickerClosedMsg{} }},
	})
	keys := m.shell.Keys()
	m.shell = m.shell.SetShortHelp([]key.Binding{
		key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑↓", "navigate")),
		key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all")),
		keys.Tab,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "import")),
		keys.Close,
	})
	return m
}

