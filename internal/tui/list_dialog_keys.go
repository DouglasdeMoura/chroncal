package tui

import (
	"charm.land/bubbles/v2/key"
)

// ListDialogKeys is the minimal key map the shell understands. Callers embed
// it in their own dialog-specific key map and wire additional hotkeys
// (e.g. Edit/Delete/RSVP) on top.
type ListDialogKeys struct {
	Up       key.Binding
	Down     key.Binding
	Tab      key.Binding
	ShiftTab key.Binding
	Enter    key.Binding
	Close    key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding
}

func defaultListDialogKeys() ListDialogKeys {
	return ListDialogKeys{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "sections")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev section")),
		Enter:    key.NewBinding(key.WithKeys("enter", "space"), key.WithHelp("enter", "select")),
		Close:    key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "close")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f")),
		Home:     key.NewBinding(key.WithKeys("home")),
		End:      key.NewBinding(key.WithKeys("end")),
	}
}

// Keys exposes the shell's default bindings so callers can compose ShortHelp.
func (m ListDialogModel) Keys() ListDialogKeys { return m.keys }
