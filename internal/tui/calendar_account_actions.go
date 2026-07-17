package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// CalendarAccountActionsRequestedMsg opens the linked calendar's account menu.
type CalendarAccountActionsRequestedMsg struct{}

// CalendarAccountMenuClosedMsg closes the account menu without choosing an action.
type CalendarAccountMenuClosedMsg struct{}

// CalendarAccountMenuSelectedMsg closes the menu before dispatching Message.
type CalendarAccountMenuSelectedMsg struct {
	Message tea.Msg
}

// calendarAccountActionMessages bundles the optional messages an Account
// menu can dispatch. Every field is optional; a nil field omits the
// corresponding action. Both the calendar edit dialog's Account menu and
// the sidebar's Account dialog assemble through buildCalendarAccountActions
// so ordering and danger styling stay in one place.
type calendarAccountActionMessages struct {
	Manage     tea.Msg // CalendarDiscoverAdditionalRequestedMsg, or nil to hide
	Reauth     tea.Msg // CalendarReauthRequestedMsg, or nil to hide
	Disconnect tea.Msg // CalendarDisconnectRemoteRequestedMsg, or nil to hide
}

type calendarAccountMenuAction struct {
	label   string
	variant ButtonVariant
	onPress func() tea.Msg
}

// buildCalendarAccountActions assembles the Account menu's action list from
// optional explicit messages. Non-nil messages land in fixed order — Manage,
// Re-authenticate, Disconnect — and Cancel is always appended last.
// Disconnect is the only destructive entry (ButtonDanger); the rest stay
// neutral. An empty message set yields a Cancel-only list, which is the
// menu's no-draft fallback.
func buildCalendarAccountActions(msgs calendarAccountActionMessages) []calendarAccountMenuAction {
	actions := make([]calendarAccountMenuAction, 0, 4)
	if msgs.Manage != nil {
		manage := msgs.Manage
		actions = append(actions, calendarAccountMenuAction{
			label:   "Manage calendars…",
			onPress: func() tea.Msg { return manage },
		})
	}
	if msgs.Reauth != nil {
		reauth := msgs.Reauth
		actions = append(actions, calendarAccountMenuAction{
			label:   "Re-authenticate…",
			onPress: func() tea.Msg { return reauth },
		})
	}
	if msgs.Disconnect != nil {
		disconnect := msgs.Disconnect
		actions = append(actions, calendarAccountMenuAction{
			label:   "Disconnect…",
			variant: ButtonDanger,
			onPress: func() tea.Msg { return disconnect },
		})
	}
	actions = append(actions, calendarAccountMenuAction{
		label:   "Cancel",
		onPress: func() tea.Msg { return CalendarAccountMenuClosedMsg{} },
	})
	return actions
}

// CalendarAccountActionsMenuModel presents infrequent account maintenance
// actions without crowding the calendar edit dialog's commit controls.
type CalendarAccountActionsMenuModel struct {
	dialog   Dialog
	help     help.Model
	actions  []calendarAccountMenuAction
	selected int
	buttons  ButtonStyles
}

const calendarAccountActionsMaxWidth = 40

func newCalendarAccountActionsMenu(theme Theme, actions []calendarAccountMenuAction) CalendarAccountActionsMenuModel {
	dialog := NewDialog("Account", DefaultDialogStyles())
	return CalendarAccountActionsMenuModel{
		dialog:  dialog,
		help:    newThemedHelp(theme),
		actions: actions,
		buttons: DefaultButtonStyles(),
	}
}

func (m CalendarAccountActionsMenuModel) SetSize(w, h int) CalendarAccountActionsMenuModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	dw := min(w, calendarAccountActionsMaxWidth)
	m.dialog.SetWidth(dw)
	return m
}

func (m CalendarAccountActionsMenuModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m CalendarAccountActionsMenuModel) activateSelected() tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.actions) {
		return nil
	}
	msg := m.actions[m.selected].onPress()
	return func() tea.Msg { return CalendarAccountMenuSelectedMsg{Message: msg} }
}

func (m CalendarAccountActionsMenuModel) Update(msg tea.Msg) (CalendarAccountActionsMenuModel, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	if mc, ok := msg.(tea.MouseClickMsg); ok {
		if mc.Button != tea.MouseLeft {
			return m, nil
		}
		bw, bh := m.BoxSize()
		ox := (m.dialog.width - bw) / 2
		oy := (m.dialog.height - bh) / 2
		target := mouseResolve(mc.X-ox, mc.Y-oy)
		for i := range m.actions {
			if target == calendarAccountActionTarget(i) {
				m.selected = i
				return m, m.activateSelected()
			}
		}
		return m, nil
	}

	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc", "q"))):
			return m, func() tea.Msg { return CalendarAccountMenuClosedMsg{} }
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k", "shift+tab"))):
			m.selected = (m.selected - 1 + len(m.actions)) % len(m.actions)
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j", "tab"))):
			m.selected = (m.selected + 1) % len(m.actions)
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter", " "))):
			return m, m.activateSelected()
		}
	}

	return m, nil
}

func (m CalendarAccountActionsMenuModel) View() string {
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "select")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))

	width := max(m.dialog.ContentWidth()-1, 1)
	rows := make([]string, 0, len(m.actions)+2)
	for i, action := range m.actions {
		if action.variant == ButtonDanger && i > 0 {
			rows = append(rows, lipgloss.NewStyle().Foreground(activeTheme.TextDim).Render(strings.Repeat("─", width)))
		}
		style := m.buttons.Get(action.variant).Normal
		if i == m.selected {
			style = m.buttons.Get(action.variant).Focused
		}
		style = style.MarginRight(0).Width(width)
		button := mouseMark(calendarAccountActionTarget(i), style.Render(action.label))
		rows = append(rows, button)
	}
	return mouseSweep(m.dialog.Box(strings.Join(rows, "\n")))
}

func calendarAccountActionTarget(index int) string {
	return fmt.Sprintf("calendar-account-action:%d", index)
}
