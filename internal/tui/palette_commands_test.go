package tui

import "testing"

func TestPaletteExposesCalendarAddWithoutAccountManagement(t *testing.T) {
	commands := buildPaletteCommands(Model{})
	foundCalendarAdd := false
	for _, command := range commands {
		switch command.ID {
		case "calendar.new":
			foundCalendarAdd = true
		case "account.add", "account.manage":
			t.Fatalf("standalone account command %q should not be exposed", command.ID)
		}
	}
	if !foundCalendarAdd {
		t.Fatal("Add Calendar command is missing")
	}
}
