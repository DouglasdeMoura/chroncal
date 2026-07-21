package tui

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
)

func TestPaletteSeparatesNewCalendarFromAddAccount(t *testing.T) {
	commands := buildPaletteCommands(Model{})

	newCalendar := paletteCommandByID(t, commands, "calendar.new")
	if newCalendar.Title != "New Calendar" {
		t.Fatalf("calendar.new title = %q, want New Calendar", newCalendar.Title)
	}
	if _, ok := newCalendar.Action().(CalendarDialogRequestedMsg); !ok {
		t.Fatalf("calendar.new action = %T, want CalendarDialogRequestedMsg", newCalendar.Action())
	}

	addAccount := paletteCommandByID(t, commands, "account.add")
	if addAccount.Title != "Add Account…" || addAccount.Category != "Account" {
		t.Fatalf("account.add = %+v", addAccount)
	}
	if _, ok := addAccount.Action().(AccountAddRequestedMsg); !ok {
		t.Fatalf("account.add action = %T, want AccountAddRequestedMsg", addAccount.Action())
	}
}

func TestPaletteExposesAccountSettingsByAccountIdentity(t *testing.T) {
	commands := buildPaletteCommands(Model{accounts: map[int64]account.Account{
		9: {ID: 9, DisplayName: "Work", DisplayOrder: 1},
		7: {ID: 7, DisplayName: "Personal Google", DisplayOrder: 0},
	}})

	personal := paletteCommandByID(t, commands, "account.manage.7")
	work := paletteCommandByID(t, commands, "account.manage.9")
	if personal.Title != "Manage Personal Google…" || work.Title != "Manage Work…" {
		t.Fatalf("account titles = %q, %q", personal.Title, work.Title)
	}
	if personal.Category != "Account" || work.Category != "Account" {
		t.Fatalf("account categories = %q, %q", personal.Category, work.Category)
	}
	msg, ok := personal.Action().(AccountSettingsRequestedMsg)
	if !ok || msg.AccountID != 7 {
		t.Fatalf("Personal action = %#v, want account 7 settings", personal.Action())
	}

	personalIndex := paletteCommandIndex(commands, personal.ID)
	workIndex := paletteCommandIndex(commands, work.ID)
	if personalIndex >= workIndex {
		t.Fatalf("account command order = Personal %d, Work %d; want display order", personalIndex, workIndex)
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
	if !m.calendarDialogOpen {
		t.Fatal("Add Account did not open its dialog")
	}
	view := stripANSI(m.calendarDialog.View())
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

func paletteCommandIndex(commands []PaletteCommand, id string) int {
	for i, command := range commands {
		if command.ID == id {
			return i
		}
	}
	return -1
}
