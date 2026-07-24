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

// TestPaletteExposesNewCalendarAndAddAccount locks issue #552's
// discoverability: the manager's + Add actions (New Calendar, Add Account)
// are reachable from the command palette. Each entry emits the single
// canonical CalendarManagerRequestedMsg with the corresponding manager
// target — there is no bespoke message per entry point.
func TestPaletteExposesNewCalendarAndAddAccount(t *testing.T) {
	commands := buildPaletteCommands(Model{})

	create := paletteCommandByID(t, commands, "calendar.create")
	if create.Title != "New Calendar…" || create.Category != "Calendar" {
		t.Fatalf("calendar.create = %+v, want title New Calendar… in Calendar", create)
	}
	if msg, ok := create.Action().(CalendarManagerRequestedMsg); !ok || msg.Target != CalendarManagerTargetLocalCreate {
		t.Fatalf("calendar.create action = %T %+v, want LocalCreate request", create.Action(), create.Action())
	}

	connect := paletteCommandByID(t, commands, "calendar.add_account")
	if connect.Title != "Add Account…" || connect.Category != "Calendar" {
		t.Fatalf("calendar.add_account = %+v, want title Add Account… in Calendar", connect)
	}
	if msg, ok := connect.Action().(CalendarManagerRequestedMsg); !ok || msg.Target != CalendarManagerTargetAccountConnect {
		t.Fatalf("calendar.add_account action = %T %+v, want AccountConnect request", connect.Action(), connect.Action())
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

// TestPaletteAddAccountOpensConnectionForm is the direct message-flow smoke
// for the Add Account palette entry: the canonical CalendarManagerRequestedMsg
// it emits opens the manager's account-connection form rendering the Sign In
// surface. (AccountAddRequestedMsg was removed; every Add Account entry point
// now emits the single canonical request.)
func TestPaletteAddAccountOpensConnectionForm(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40

	updated, cmd := m.Update(CalendarManagerRequestedMsg{Target: CalendarManagerTargetAccountConnect})
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
