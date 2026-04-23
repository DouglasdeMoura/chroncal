package tui

import "testing"

func TestDefaultAgendaKeys_ReservesEForEdit(t *testing.T) {
	keys := defaultAgendaKeys()

	if got := keys.ToggleEmpty.Keys(); len(got) != 1 || got[0] != "o" {
		t.Fatalf("ToggleEmpty keys = %v, want [o]", got)
	}

	help := keys.ToggleEmpty.Help()
	if help.Key != "o" {
		t.Fatalf("ToggleEmpty help key = %q, want %q", help.Key, "o")
	}
	if help.Desc != "empty days" {
		t.Fatalf("ToggleEmpty help desc = %q, want %q", help.Desc, "empty days")
	}
}
