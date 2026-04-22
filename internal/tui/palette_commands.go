package tui

import (
	tea "charm.land/bubbletea/v2"
)

// Palette action messages. The palette emits these via a selected command's
// Action func; the app-level Update handles them like any other tea.Msg.

// SwitchViewMsg requests a view-mode change.
type SwitchViewMsg struct{ Mode viewMode }

// GoToTodayMsg jumps the active view's cursor to today.
type GoToTodayMsg struct{}

// ToggleSidebarMsg toggles the sidebar panel.
type ToggleSidebarMsg struct{}

// buildPaletteCommands returns the default commands exposed through the
// palette, with bindings to the current app state (cursor, etc.).
func buildPaletteCommands(m Model) []PaletteCommand {
	cursor, _ := m.viewCursorAndToday()

	return []PaletteCommand{
		{
			ID:       "nav.today",
			Title:    "Go to Today",
			Category: "Navigation",
			Shortcut: "t",
			Action:   func() tea.Msg { return GoToTodayMsg{} },
		},
		{
			ID:       "event.new",
			Title:    "Create Event",
			Category: "Event",
			Shortcut: "c",
			Action:   func() tea.Msg { return EventCreateMsg{Day: cursor} },
		},
		{
			ID:       "view.month",
			Title:    "Month View",
			Category: "View",
			Shortcut: "m",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewMonth} },
		},
		{
			ID:       "view.week",
			Title:    "Week View",
			Category: "View",
			Shortcut: "w",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewWeek} },
		},
		{
			ID:       "view.day",
			Title:    "Day View",
			Category: "View",
			Shortcut: "d",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewDay} },
		},
		{
			ID:       "view.agenda",
			Title:    "Agenda View",
			Category: "View",
			Shortcut: "a",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewAgenda} },
		},
		{
			ID:       "calendar.new",
			Title:    "Add Calendar",
			Category: "Calendar",
			Shortcut: "l",
			Action:   func() tea.Msg { return CalendarDialogRequestedMsg{ID: 0} },
		},
		{
			ID:       "calendar.manage",
			Title:    "Calendars",
			Category: "Calendar",
			Shortcut: "r",
			Action:   func() tea.Msg { return CalendarListDialogRequestedMsg{} },
		},
		{
			ID:       "calendar.sync",
			Title:    "Sync All Calendars",
			Category: "Calendar",
			Shortcut: "s",
			Action:   func() tea.Msg { return SyncAllRequestedMsg{} },
		},
		{
			ID:       "ui.sidebar",
			Title:    "Toggle Sidebar",
			Category: "View",
			Shortcut: "\\",
			Action:   func() tea.Msg { return ToggleSidebarMsg{} },
		},
		{
			ID:       "ui.help",
			Title:    "Help",
			Category: "View",
			Shortcut: "?",
			Action:   func() tea.Msg { return HelpDialogRequestedMsg{} },
		},
		{
			ID:       "app.quit",
			Title:    "Quit",
			Category: "App",
			Shortcut: "q",
			Action:   func() tea.Msg { return tea.QuitMsg{} },
		},
	}
}
