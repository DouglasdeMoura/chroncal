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

// ToggleHelpMsg toggles the full-help footer.
type ToggleHelpMsg struct{}

// buildPaletteCommands returns the default commands exposed through the
// palette, with bindings to the current app state (cursor, etc.).
func buildPaletteCommands(m Model) []PaletteCommand {
	cursor, _ := m.viewCursorAndToday()

	return []PaletteCommand{
		{
			ID:       "event.new",
			Title:    "New event",
			Category: "Event",
			Shortcut: "c",
			Action:   func() tea.Msg { return EventCreateMsg{Day: cursor} },
		},
		{
			ID:       "view.month",
			Title:    "Switch to Month view",
			Category: "View",
			Shortcut: "m",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewMonth} },
		},
		{
			ID:       "view.week",
			Title:    "Switch to Week view",
			Category: "View",
			Shortcut: "w",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewWeek} },
		},
		{
			ID:       "view.day",
			Title:    "Switch to Day view",
			Category: "View",
			Shortcut: "d",
			Action:   func() tea.Msg { return SwitchViewMsg{Mode: viewDay} },
		},
		{
			ID:       "nav.today",
			Title:    "Go to today",
			Category: "Navigation",
			Shortcut: "t",
			Action:   func() tea.Msg { return GoToTodayMsg{} },
		},
		{
			ID:       "ui.sidebar",
			Title:    "Toggle sidebar",
			Category: "View",
			Shortcut: "s",
			Action:   func() tea.Msg { return ToggleSidebarMsg{} },
		},
		{
			ID:       "ui.help",
			Title:    "Toggle help",
			Category: "View",
			Shortcut: "?",
			Action:   func() tea.Msg { return ToggleHelpMsg{} },
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
