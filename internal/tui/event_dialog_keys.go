package tui

import (
	"charm.land/bubbles/v2/key"
)

type eventDialogKeyMap struct {
	Left      key.Binding
	Right     key.Binding
	Edit      key.Binding
	Delete    key.Binding
	Duplicate key.Binding
	Create    key.Binding
	RSVPYes   key.Binding
	RSVPNo    key.Binding
	RSVPMaybe key.Binding
	Copy      key.Binding
}

func defaultEventDialogKeys() eventDialogKeyMap {
	return eventDialogKeyMap{
		Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous")),
		Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next")),
		Edit:      key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Delete:    key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "delete")),
		Duplicate: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "duplicate")),
		Create:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "create")),
		RSVPYes:   key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "RSVP yes")),
		RSVPNo:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "RSVP no")),
		RSVPMaybe: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "RSVP maybe")),
		Copy:      key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "copy")),
	}
}

// WithAppKeys replaces the create binding with the keys of the app create
// action. The help text stays "create". A remap of the create action in
// config.toml then also applies in the day dialog.
func (m EventDialogModel) WithAppKeys(keys appKeyMap) EventDialogModel {
	m.keys.Create = key.NewBinding(key.WithKeys(keys.Create.Keys()...), key.WithHelp(keyLabel(keys.Create), "create"))
	return m
}

func (m EventDialogModel) shortHelp() []key.Binding {
	sk := m.shell.Keys()
	nav := key.NewBinding(
		key.WithKeys("up", "down", "k", "j"),
		key.WithHelp("↑↓", "navigate"),
	)
	days := key.NewBinding(
		key.WithKeys("left", "right", "h", "l"),
		key.WithHelp("←→", "days"),
	)
	if len(m.events) == 0 {
		return []key.Binding{days, sk.Enter, m.keys.Create, sk.Close}
	}
	return []key.Binding{nav, days, sk.Tab, m.keys.Create, m.keys.Delete, sk.Close}
}
