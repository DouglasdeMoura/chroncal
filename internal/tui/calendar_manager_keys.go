package tui

import (
	"charm.land/bubbles/v2/key"
)

type calendarManagerKeyMap struct {
	Open  key.Binding
	Close key.Binding
	Add   key.Binding
	// Next/Prev cycle the Apple-style root focus ring (Tab/Shift-Tab) before
	// any list child sees the key. Activate fires Enter/Space on the focused
	// source or inspector action.
	Next     key.Binding
	Prev     key.Binding
	Activate key.Binding
}

func defaultCalendarManagerKeys() calendarManagerKeyMap {
	return calendarManagerKeyMap{
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Close:    key.NewBinding(key.WithKeys("esc", "q", "C", "shift+c"), key.WithHelp("esc", "close")),
		Add:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		Next:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		Prev:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		Activate: key.NewBinding(key.WithKeys("enter", "space"), key.WithHelp("enter/space", "activate")),
	}
}

// WithAppKeys makes the close binding follow the app keymap. Esc and q
// close the manager. The keys of the app calendars action also close it,
// so the key that opens the manager also closes it after a remap.
func (m CalendarManagerModel) WithAppKeys(keys appKeyMap) CalendarManagerModel {
	closeKeys := append([]string{"esc", "q"}, keys.CalendarList.Keys()...)
	m.keys.Close = key.NewBinding(key.WithKeys(closeKeys...), key.WithHelp("esc", "close"))
	return m
}
