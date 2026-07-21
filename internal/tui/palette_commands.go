package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

// Palette action messages. The palette emits these via a selected command's
// Action func; the app-level Update handles them like any other tea.Msg.

// SwitchViewMsg requests a view-mode change.
type SwitchViewMsg struct{ Mode viewMode }

// GoToTodayMsg jumps the active view's cursor to today.
type GoToTodayMsg struct{}

// ToggleSidebarMsg toggles the sidebar panel.
type ToggleSidebarMsg struct{}

// ToggleWeekNumbersMsg toggles the ISO week-number gutter in month/week views.
type ToggleWeekNumbersMsg struct{}

// AccountAddRequestedMsg opens the account sign-in flow.
type AccountAddRequestedMsg struct{}

// buildPaletteCommands returns the default commands exposed through the
// palette, with bindings to the current app state (cursor, accounts, etc.).
func buildPaletteCommands(m Model) []PaletteCommand {
	cursor, _ := m.viewCursorAndToday()
	commands := make([]PaletteCommand, 0, 15+len(m.accounts))
	commands = append(commands,
		PaletteCommand{
			ID:       "nav.today",
			Title:    "Go to Today",
			Category: "Navigation",
			Shortcut: "t",
			Action:   func() tea.Msg { return GoToTodayMsg{} },
		},
		PaletteCommand{
			ID:       "event.new",
			Title:    "Create Event",
			Category: "Event",
			Shortcut: "c",
			Action:   func() tea.Msg { return EventCreateMsg{Day: cursor} },
		},
		PaletteCommand{
			ID:       "view.month",
			Title:    "Month View",
			Category: "View",
			Shortcut: "m",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewMonth} },
		},
		PaletteCommand{
			ID:       "view.week",
			Title:    "Week View",
			Category: "View",
			Shortcut: "w",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewWeek} },
		},
		PaletteCommand{
			ID:       "view.day",
			Title:    "Day View",
			Category: "View",
			Shortcut: "d",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewDay} },
		},
		PaletteCommand{
			ID:       "view.agenda",
			Title:    "Agenda View",
			Category: "View",
			Shortcut: "a",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewAgenda} },
		},
		PaletteCommand{
			ID:       "calendar.new",
			Title:    "New Calendar",
			Category: "Calendar",
			Shortcut: "l",
			Action:   func() tea.Msg { return CalendarDialogRequestedMsg{ID: 0} },
		},
		PaletteCommand{
			ID:       "account.add",
			Title:    "Add Account…",
			Category: "Account",
			Action:   func() tea.Msg { return AccountAddRequestedMsg{} },
		},
	)
	commands = append(commands, accountManagementPaletteCommands(m)...)
	commands = append(commands,
		PaletteCommand{
			ID:       "calendar.manage",
			Title:    "Manage Calendars…",
			Category: "Calendar",
			Shortcut: "C",
			Action:   func() tea.Msg { return CalendarListDialogRequestedMsg{} },
		},
		PaletteCommand{
			ID:       "calendar.sync",
			Title:    "Sync All Calendars",
			Category: "Calendar",
			Shortcut: "s",
			Action:   func() tea.Msg { return SyncAllRequestedMsg{} },
		},
		PaletteCommand{
			ID:       "ui.sidebar",
			Title:    "Toggle Sidebar",
			Category: "View",
			Shortcut: "\\",
			Action:   func() tea.Msg { return ToggleSidebarMsg{} },
		},
		PaletteCommand{
			ID:       "ui.week_numbers",
			Title:    "Toggle Week Numbers",
			Category: "View",
			Shortcut: "#",
			Action:   func() tea.Msg { return ToggleWeekNumbersMsg{} },
		},
		PaletteCommand{
			ID:       "trash.view",
			Title:    "Recently Deleted",
			Category: "View",
			Shortcut: "D",
			Action:   func() tea.Msg { return TrashViewRequestedMsg{} },
		},
		PaletteCommand{
			ID:       "ui.help",
			Title:    "Help",
			Category: "View",
			Shortcut: "?",
			Action:   func() tea.Msg { return HelpDialogRequestedMsg{} },
		},
		PaletteCommand{
			ID:       "app.quit",
			Title:    "Quit",
			Category: "App",
			Shortcut: "q",
			Action:   func() tea.Msg { return tea.QuitMsg{} },
		},
	)
	return commands
}

func accountManagementPaletteCommands(m Model) []PaletteCommand {
	ids := make([]int64, 0, len(m.accounts))
	for id := range m.accounts {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int64) int {
		left, right := m.accounts[a], m.accounts[b]
		return cmp.Or(
			cmp.Compare(left.DisplayOrder, right.DisplayOrder),
			strings.Compare(strings.ToLower(left.DisplayName), strings.ToLower(right.DisplayName)),
			cmp.Compare(a, b),
		)
	})

	commands := make([]PaletteCommand, 0, len(ids))
	for _, id := range ids {
		configured := m.accounts[id]
		name := strings.TrimSpace(textsafe.Display(configured.DisplayName))
		if name == "" {
			name = strings.TrimSpace(textsafe.Display(configured.Username))
		}
		if name == "" {
			name = fmt.Sprintf("Account %d", id)
		}
		accountID := id
		commands = append(commands, PaletteCommand{
			ID:       fmt.Sprintf("account.manage.%d", accountID),
			Title:    "Manage " + name + "…",
			Category: "Account",
			Action: func() tea.Msg {
				return AccountSettingsRequestedMsg{AccountID: accountID}
			},
		})
	}
	return commands
}
