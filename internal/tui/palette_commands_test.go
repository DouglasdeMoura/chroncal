package tui

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
)

func TestPaletteSeparatesNewCalendarFromAddAccount(t *testing.T) {
	commands := buildPaletteCommands(Model{})

	newCalendar := paletteCommandByID(t, commands, "calendar.new")
	if newCalendar.Title != "New Local Calendar" {
		t.Fatalf("calendar.new title = %q, want New Local Calendar", newCalendar.Title)
	}
	request, ok := newCalendar.Action().(CalendarManagerRequestedMsg)
	if !ok || request.Target != CalendarManagerTargetLocalCreate {
		t.Fatalf("calendar.new action = %#v, want local-create manager request", newCalendar.Action())
	}

	addAccount := paletteCommandByID(t, commands, "account.add")
	if addAccount.Title != "Add Account…" || addAccount.Category != "Account" {
		t.Fatalf("account.add = %+v", addAccount)
	}
	request, ok = addAccount.Action().(CalendarManagerRequestedMsg)
	if !ok || request.Target != CalendarManagerTargetAccountConnect {
		t.Fatalf("account.add action = %#v, want account-connect manager request", addAccount.Action())
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
