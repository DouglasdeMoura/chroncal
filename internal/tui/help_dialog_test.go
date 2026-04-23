package tui

import "testing"

func TestHelpDialog_NavigationListsAgendaToggleOnO(t *testing.T) {
	sections := NewHelpDialogModel(NewTheme(true)).sections()

	for _, section := range sections {
		if section.title != "Navigation" {
			continue
		}
		for _, entry := range section.entries {
			if entry.desc == "toggle empty days (agenda)" {
				if entry.keys != "o" {
					t.Fatalf("navigation toggle key = %q, want %q", entry.keys, "o")
				}
				return
			}
		}
		t.Fatal("navigation section missing empty-day toggle entry")
	}

	t.Fatal("missing navigation section")
}
