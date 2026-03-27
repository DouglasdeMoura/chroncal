package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up             key.Binding
	Down           key.Binding
	Left           key.Binding
	Right          key.Binding
	Today          key.Binding
	Enter          key.Binding
	Back           key.Binding
	NewEvent       key.Binding
	NewTodo        key.Binding
	Edit           key.Binding
	Delete         key.Binding
	ToggleComplete key.Binding
	MonthView      key.Binding
	WeekView       key.Binding
	DayView        key.Binding
	AgendaView     key.Binding
	ToggleSidebar  key.Binding
	Help           key.Binding
	Quit           key.Binding
	NextMonth      key.Binding
	PrevMonth      key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("k", "up"),
		key.WithHelp("k/↑", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("j", "down"),
		key.WithHelp("j/↓", "down"),
	),
	Left: key.NewBinding(
		key.WithKeys("h", "left"),
		key.WithHelp("h/←", "left"),
	),
	Right: key.NewBinding(
		key.WithKeys("l", "right"),
		key.WithHelp("l/→", "right"),
	),
	Today: key.NewBinding(
		key.WithKeys("g"),
		key.WithHelp("g", "go to today"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
	),
	NewEvent: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new event"),
	),
	NewTodo: key.NewBinding(
		key.WithKeys("t"),
		key.WithHelp("t", "new todo"),
	),
	ToggleComplete: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "toggle done"),
	),
	Edit: key.NewBinding(
		key.WithKeys("e"),
		key.WithHelp("e", "edit"),
	),
	Delete: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "delete"),
	),
	MonthView: key.NewBinding(
		key.WithKeys("1"),
		key.WithHelp("1", "month"),
	),
	WeekView: key.NewBinding(
		key.WithKeys("2"),
		key.WithHelp("2", "week"),
	),
	DayView: key.NewBinding(
		key.WithKeys("3"),
		key.WithHelp("3", "day"),
	),
	AgendaView: key.NewBinding(
		key.WithKeys("4"),
		key.WithHelp("4", "agenda"),
	),
	ToggleSidebar: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "sidebar"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	NextMonth: key.NewBinding(
		key.WithKeys("L", "shift+right"),
		key.WithHelp("L", "next month"),
	),
	PrevMonth: key.NewBinding(
		key.WithKeys("H", "shift+left"),
		key.WithHelp("H", "prev month"),
	),
}
