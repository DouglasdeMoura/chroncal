package tui

import (
	"charm.land/bubbles/v2/key"
)

type appKeyMap struct {
	Quit         key.Binding
	MonthView    key.Binding
	WeekView     key.Binding
	DayView      key.Binding
	AgendaView   key.Binding
	Sidebar      key.Binding
	WeekNumbers  key.Binding
	WeekStart    key.Binding
	Create       key.Binding
	SwitchFocus  key.Binding
	Help         key.Binding
	Palette      key.Binding
	CalendarList key.Binding
	Sync         key.Binding
	Undo         key.Binding
	TrashView    key.Binding
}

func defaultAppKeys() appKeyMap {
	return appKeyMap{
		Quit:         key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		MonthView:    key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "month")),
		WeekView:     key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "week")),
		DayView:      key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "day")),
		AgendaView:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "agenda")),
		Sidebar:      key.NewBinding(key.WithKeys("\\"), key.WithHelp("\\", "sidebar")),
		WeekNumbers:  key.NewBinding(key.WithKeys("#"), key.WithHelp("#", "week numbers")),
		WeekStart:    key.NewBinding(key.WithKeys("W", "shift+w"), key.WithHelp("W", "week start")),
		Create:       key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new")),
		SwitchFocus:  key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "switch focus")),
		Help:         key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Palette:      key.NewBinding(key.WithKeys("/", "ctrl+k"), key.WithHelp("/", "commands")),
		CalendarList: key.NewBinding(key.WithKeys("C", "shift+c"), key.WithHelp("C", "calendars")),
		Sync:         key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
		Undo:         key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "undo")),
		TrashView:    key.NewBinding(key.WithKeys("D", "shift+d"), key.WithHelp("D", "recently deleted")),
	}
}
