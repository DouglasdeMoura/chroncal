package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
)

// helpRequested reports whether a key press asks for the help dialog.
func helpRequested(m Model, k string) bool {
	_, cmd := m.Update(keyPressMsg(k))
	if cmd == nil {
		return false
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				if _, ok := c().(HelpDialogRequestedMsg); ok {
					return true
				}
			}
		}
		return false
	}
	_, ok := msg.(HelpDialogRequestedMsg)
	return ok
}

// TestRemappedHelpKeyDoesNotFireInTextFields remaps the help action to the
// plain letter "i". The letter must reach a text field: the Rename account
// field and the command palette. Over a dialog with no text field, such as
// the day dialog, the key still opens the help.
func TestRemappedHelpKeyDoesNotFireInTextFields(t *testing.T) {
	base := func() Model {
		m := NewModel(nil, "")
		m.width, m.height = 120, 40
		return m.withKeys(newAppKeys(map[string][]string{"help": {"i"}}))
	}

	rename := base()
	rename.accountRename = newAccountRenameDialogModel(account.Account{ID: 1, DisplayName: "Work"}, rename.theme)
	rename.accountRenameOpen = true
	if helpRequested(rename, "i") {
		t.Fatal("help key fired in the Rename account field")
	}

	palette := base()
	palette.paletteOpen = true
	if helpRequested(palette, "i") {
		t.Fatal("help key fired in the command palette")
	}

	day := base()
	day.dialogOpen = true
	day.dialog = day.newDayDialog(day.calendar.Cursor(), nil)
	if !helpRequested(day, "i") {
		t.Fatal("help key did not fire over the day dialog")
	}
}
