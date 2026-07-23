package tui

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
)

func TestPaletteExposesOneCalendarsEntry(t *testing.T) {
	commands := buildPaletteCommands(Model{})
	manage := paletteCommandByID(t, commands, "calendar.manage")
	if manage.Title != "Calendars" || manage.Shortcut != "C" {
		t.Fatalf("calendar.manage = %+v, want Calendars with C shortcut", manage)
	}
	for _, command := range commands {
		if command.ID == "calendar.new" || command.ID == "account.add" {
			t.Fatalf("palette exposes obsolete calendar entry %q", command.ID)
		}
	}
}

func TestPaletteDoesNotExposeIndividualAccountManagement(t *testing.T) {
	commands := buildPaletteCommands(Model{accounts: map[int64]account.Account{
		9: {ID: 9, DisplayName: "Work", DisplayOrder: 1},
		7: {ID: 7, DisplayName: "Personal Google", DisplayOrder: 0},
	}})

	for _, command := range commands {
		if strings.HasPrefix(command.ID, "account.manage.") {
			t.Fatalf("palette exposes individual account command %q", command.ID)
		}
	}
}

func TestAccountAddRequestOpensSignInDialog(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40

	updated, cmd := m.Update(AccountAddRequestedMsg{})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("opening Add Account returned command %T", cmd())
	}
	if !m.calendarManagerOpen {
		t.Fatal("Add Account did not open its dialog")
	}
	view := stripANSI(m.calendarManager.View())
	if !strings.Contains(view, "Add Account") || !strings.Contains(view, "Sign In") {
		t.Fatalf("Add Account opened the wrong surface:\n%s", view)
	}
}

func paletteCommandByID(t *testing.T, commands []PaletteCommand, id string) PaletteCommand {
	t.Helper()
	for _, command := range commands {
		if command.ID == id {
			return command
		}
	}
	t.Fatalf("palette command %q is missing", id)
	return PaletteCommand{}
}
