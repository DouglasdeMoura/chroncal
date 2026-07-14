package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

// AccountListDialogRequestedMsg opens account management.
type AccountListDialogRequestedMsg struct{}

// AccountListDialogClosedMsg closes account management.
type AccountListDialogClosedMsg struct{}

// AccountRefreshRequestedMsg re-runs collection discovery for one account.
type AccountRefreshRequestedMsg struct{ AccountID int64 }

// AccountRemoveRequestedMsg asks the app to confirm removal. Removing an
// account disconnects its calendars but preserves their local data.
type AccountRemoveRequestedMsg struct {
	AccountID int64
	Name      string
}

// AccountListDialogModel renders configured accounts and account-scoped
// actions using the shared list/detail shell.
type AccountListDialogModel struct {
	accounts  []account.Account
	calendars map[int64]CalendarInfo
	shell     ListDialogModel
	theme     Theme
}

func NewAccountListDialogModel(accounts []account.Account, calendars map[int64]CalendarInfo, theme Theme) AccountListDialogModel {
	m := AccountListDialogModel{
		accounts:  append([]account.Account(nil), accounts...),
		calendars: calendars,
		shell: NewListDialogModel(newThemedHelp(theme)).
			SetTitle("CalDAV accounts").
			SetSelectedColor(theme.Selected),
		theme: theme,
	}
	return m.refresh()
}

func (m AccountListDialogModel) SetSize(w, h int) AccountListDialogModel {
	m.shell = m.shell.SetSize(w, h)
	return m.refresh()
}

func (m AccountListDialogModel) BoxSize() (int, int) { return m.shell.BoxSize() }
func (m AccountListDialogModel) View() string        { return m.shell.View() }

func (m AccountListDialogModel) selectedAccount() (account.Account, bool) {
	idx := m.shell.Selected()
	if idx < 0 || idx >= len(m.accounts) {
		return account.Account{}, false
	}
	return m.accounts[idx], true
}

func (m AccountListDialogModel) actions() []ListDialogAction {
	selected, ok := m.selectedAccount()
	if !ok {
		return nil
	}
	return []ListDialogAction{
		{Label: "Refresh", Primary: true, Msg: func() tea.Msg { return AccountRefreshRequestedMsg{AccountID: selected.ID} }},
		{Label: "Remove", Danger: true, Msg: func() tea.Msg {
			return AccountRemoveRequestedMsg{AccountID: selected.ID, Name: selected.DisplayName}
		}},
	}
}

func (m AccountListDialogModel) Update(msg tea.Msg) (AccountListDialogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.SetSize(msg.Width, msg.Height), nil
	case tea.KeyPressMsg:
		actions := m.actions()
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("r"))):
			if len(actions) > 0 {
				m.shell = m.shell.FocusAction(0)
				return m.refresh(), actions[0].Msg
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("d"))):
			if len(actions) > 1 {
				m.shell = m.shell.FocusAction(1)
				return m.refresh(), actions[1].Msg
			}
		}
		var cmd tea.Cmd
		var handled bool
		m.shell, cmd, handled = m.shell.HandleKey(msg, func() tea.Msg { return AccountListDialogClosedMsg{} })
		if handled {
			return m.refresh(), cmd
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		if idx, ok := m.shell.RowAtPosition(msg.X, msg.Y); ok {
			m.shell = m.shell.SetFocusZone(ListZoneList).SetSelected(idx)
			return m.refresh(), nil
		}
		if idx, ok := m.shell.ActionAtPosition(msg.X, msg.Y); ok {
			actions := m.actions()
			m.shell = m.shell.FocusAction(idx)
			return m.refresh(), actions[idx].Msg
		}
		if cmd, ok := m.shell.TitleActionAtPosition(msg.X, msg.Y); ok {
			return m, cmd
		}
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.shell, cmd = m.shell.HandleMouseWheel(msg)
		return m, cmd
	}
	return m, nil
}

func (m AccountListDialogModel) refresh() AccountListDialogModel {
	rows := make([]string, 0, len(m.accounts))
	for _, remoteAccount := range m.accounts {
		hasError := false
		for _, calendarInfo := range m.calendars {
			if calendarInfo.AccountID == remoteAccount.ID && (calendarInfo.RemoteMissing || calendarInfo.LastSyncError != "") {
				hasError = true
				break
			}
		}
		row := "● " + textsafe.Display(remoteAccount.DisplayName)
		if hasError {
			row += " " + lipgloss.NewStyle().Foreground(m.theme.Error).Render("⚠")
		}
		rows = append(rows, row)
	}
	m.shell = m.shell.SetRows(rows)
	m.shell = m.shell.SetTitleAction(&ListDialogAction{
		Label: "Add Account",
		Msg:   func() tea.Msg { return AccountDialogRequestedMsg{} },
	})

	selected, ok := m.selectedAccount()
	if !ok {
		m.shell = m.shell.
			SetEmptyList("No CalDAV accounts.", []string{"Add an account to discover all calendar collections on a server."}).
			SetDetailTitle("").
			SetDetailLines(nil).
			SetActions(nil)
	} else {
		calendarCount := 0
		readOnlyCount := 0
		missingCount := 0
		for _, calendarInfo := range m.calendars {
			if calendarInfo.AccountID != selected.ID {
				continue
			}
			calendarCount++
			if calendarInfo.RemoteAccess == "read" {
				readOnlyCount++
			}
			if calendarInfo.RemoteMissing {
				missingCount++
			}
		}
		calendarLabel := "calendars"
		if calendarCount == 1 {
			calendarLabel = "calendar"
		}
		details := []string{
			fmt.Sprintf("%d %s", calendarCount, calendarLabel),
			"Server: " + textsafe.Display(selected.ServerURL),
			"Username: " + textsafe.Display(selected.Username),
			"Authentication: " + selected.AuthType,
		}
		if readOnlyCount > 0 {
			details = append(details, fmt.Sprintf("Read-only: %d", readOnlyCount))
		}
		if missingCount > 0 {
			details = append(details, lipgloss.NewStyle().Foreground(m.theme.Error).Render(fmt.Sprintf("Missing remotely: %d", missingCount)))
		}
		m.shell = m.shell.
			SetDetailTitle(textsafe.Display(selected.DisplayName)).
			SetDetailLines(details).
			SetActions(m.actions())
	}

	keys := m.shell.Keys()
	short := []key.Binding{
		key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑↓", "navigate")),
		keys.Tab,
	}
	if len(m.accounts) > 0 {
		short = append(short,
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")),
		)
	}
	short = append(short, keys.Close)
	m.shell = m.shell.SetShortHelp(short)
	return m
}

func accountRemovalPrompt(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "this account"
	}
	return fmt.Sprintf("Remove %q? Downloaded calendars and events stay on this device, but remote links and credentials are removed.", name)
}
