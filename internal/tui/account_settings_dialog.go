package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// AccountSettingsParams contains display-only account context. AccountID is
// the behavioral identity; DisplayName is never used to select or mutate an
// account.
type AccountSettingsParams struct {
	AccountID      int64
	DisplayName    string
	Provider       string
	Username       string
	CalendarCount  int
	AttentionCount int
	AuthType       string
}

// AccountSettingsRequestedMsg opens the settings panel for one sidebar
// account heading.
type AccountSettingsRequestedMsg struct{ AccountID int64 }

type AccountSettingsManageRequestedMsg struct{ AccountID int64 }

type AccountSettingsRenameRequestedMsg struct {
	AccountID   int64
	DisplayName string
}

type AccountSettingsReauthRequestedMsg struct{ AccountID int64 }

type AccountSettingsRemoveRequestedMsg struct {
	AccountID     int64
	DisplayName   string
	CalendarCount int
}

type AccountSettingsClosedMsg struct{}

type accountSettingsAction struct {
	label   string
	variant ButtonVariant
	onPress func() tea.Msg
}

// AccountSettingsDialogModel is the one account-scoped maintenance surface.
// Calendar edit dialogs deliberately do not embed these actions.
type AccountSettingsDialogModel struct {
	dialog    Dialog
	help      help.Model
	params    AccountSettingsParams
	actions   []accountSettingsAction
	selected  int
	buttons   ButtonStyles
	muted     color.Color
	attention color.Color
}

const accountSettingsMaxWidth = 48

func NewAccountSettingsDialogModel(params AccountSettingsParams, theme Theme) AccountSettingsDialogModel {
	title := strings.TrimSpace(params.DisplayName)
	if title == "" {
		title = "Account"
	}
	actions := []accountSettingsAction{
		{
			label: "Manage Calendars…",
			onPress: func() tea.Msg {
				return AccountSettingsManageRequestedMsg{AccountID: params.AccountID}
			},
		},
		{
			label: "Rename Account…",
			onPress: func() tea.Msg {
				return AccountSettingsRenameRequestedMsg{AccountID: params.AccountID, DisplayName: params.DisplayName}
			},
		},
	}
	if calendarAuthIsOAuth(params.AuthType) {
		actions = append(actions, accountSettingsAction{
			label: "Sign In Again…",
			onPress: func() tea.Msg {
				return AccountSettingsReauthRequestedMsg{AccountID: params.AccountID}
			},
		})
	}
	actions = append(actions,
		accountSettingsAction{
			label:   "Remove Account…",
			variant: ButtonDanger,
			onPress: func() tea.Msg {
				return AccountSettingsRemoveRequestedMsg{
					AccountID: params.AccountID, DisplayName: params.DisplayName, CalendarCount: params.CalendarCount,
				}
			},
		},
		accountSettingsAction{
			label:   "Done",
			onPress: func() tea.Msg { return AccountSettingsClosedMsg{} },
		},
	)
	return AccountSettingsDialogModel{
		dialog:    NewDialog(title, DefaultDialogStyles()),
		help:      newThemedHelp(theme),
		params:    params,
		actions:   actions,
		buttons:   DefaultButtonStyles(),
		muted:     theme.TextDim,
		attention: theme.Error,
	}
}

func (m AccountSettingsDialogModel) SetSize(w, h int) AccountSettingsDialogModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.dialog.SetWidth(min(w, accountSettingsMaxWidth))
	return m
}

func (m AccountSettingsDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m AccountSettingsDialogModel) activateSelected() tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.actions) {
		return nil
	}
	msg := m.actions[m.selected].onPress()
	return func() tea.Msg { return msg }
}

func (m AccountSettingsDialogModel) Update(msg tea.Msg) (AccountSettingsDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}
	if click, ok := msg.(tea.MouseClickMsg); ok {
		if click.Button != tea.MouseLeft {
			return m, nil
		}
		bw, bh := m.BoxSize()
		ox := (m.dialog.width - bw) / 2
		oy := (m.dialog.height - bh) / 2
		target := mouseResolve(click.X-ox, click.Y-oy)
		for i := range m.actions {
			if target == accountSettingsActionTarget(i) {
				m.selected = i
				return m, m.activateSelected()
			}
		}
		return m, nil
	}
	if press, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(press, key.NewBinding(key.WithKeys("esc", "q"))):
			return m, func() tea.Msg { return AccountSettingsClosedMsg{} }
		case key.Matches(press, key.NewBinding(key.WithKeys("up", "k", "shift+tab"))):
			m.selected = (m.selected - 1 + len(m.actions)) % len(m.actions)
			return m, nil
		case key.Matches(press, key.NewBinding(key.WithKeys("down", "j", "tab"))):
			m.selected = (m.selected + 1) % len(m.actions)
			return m, nil
		case key.Matches(press, key.NewBinding(key.WithKeys("enter", " "))):
			return m, m.activateSelected()
		}
	}
	return m, nil
}

func (m AccountSettingsDialogModel) identityLines() []string {
	lines := make([]string, 0, 4)
	if provider := strings.TrimSpace(m.params.Provider); provider != "" {
		lines = append(lines, provider)
	}
	if username := strings.TrimSpace(m.params.Username); username != "" {
		lines = append(lines, username)
	}
	lines = append(lines, fmt.Sprintf("%d %s", m.params.CalendarCount, accountSettingsNoun(m.params.CalendarCount)))
	if m.params.AttentionCount > 0 {
		lines = append(lines, fmt.Sprintf("Needs attention · %d %s", m.params.AttentionCount, accountSettingsNoun(m.params.AttentionCount)))
	}
	return lines
}

func accountSettingsNoun(count int) string {
	if count == 1 {
		return "calendar"
	}
	return "calendars"
}

func (m AccountSettingsDialogModel) View() string {
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "select")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "done")),
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))
	width := max(m.dialog.ContentWidth()-1, 1)
	rows := make([]string, 0, len(m.actions)+7)
	for _, line := range m.identityLines() {
		style := lipgloss.NewStyle().Foreground(m.muted)
		if strings.HasPrefix(line, "Needs attention") {
			style = lipgloss.NewStyle().Foreground(m.attention)
		}
		rows = append(rows, style.Render(truncateTo(line, width)))
	}
	rows = append(rows, "")
	for i, action := range m.actions {
		if action.variant == ButtonDanger {
			rows = append(rows, lipgloss.NewStyle().Foreground(m.muted).Render(strings.Repeat("─", width)))
		}
		if action.label == "Done" {
			rows = append(rows, "")
		}
		style := m.buttons.Get(action.variant).Normal
		if i == m.selected {
			style = m.buttons.Get(action.variant).Focused
		}
		style = style.MarginRight(0).Width(width)
		rows = append(rows, mouseMark(accountSettingsActionTarget(i), style.Render(action.label)))
	}
	return mouseSweep(m.dialog.Box(strings.Join(rows, "\n")))
}

func accountSettingsActionTarget(index int) string {
	return fmt.Sprintf("account-settings-action-%d", index)
}
