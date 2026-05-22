package tui

import "testing"

func TestHelpDialog_AgendaSectionDocumentsToggleEmptyOnO(t *testing.T) {
	if got := findHelpEntry(t, "Agenda view", "toggle empty days"); got != "o" {
		t.Fatalf("agenda toggle-empty key = %q, want %q", got, "o")
	}
}

func TestHelpDialog_AgendaSectionDocumentsEditAndDuplicate(t *testing.T) {
	if got := findHelpEntry(t, "Agenda view", "edit selected event"); got != "e" {
		t.Fatalf("agenda edit key = %q, want %q", got, "e")
	}
	if got := findHelpEntry(t, "Agenda view", "duplicate"); got != "ctrl+d" {
		t.Fatalf("agenda duplicate key = %q, want %q", got, "ctrl+d")
	}
}

func TestHelpDialog_GlobalSectionDocumentsRecentlyDeleted(t *testing.T) {
	if got := findHelpEntry(t, "Global", "recently deleted"); got != "D · shift+d" {
		t.Fatalf("recently-deleted key = %q, want %q", got, "D · shift+d")
	}
}

func TestHelpDialog_CalendarPopupDocumentsSetDefault(t *testing.T) {
	if got := findHelpEntry(t, "Calendar popup", "set as default"); got != "*" {
		t.Fatalf("set-default key = %q, want %q", got, "*")
	}
}

func TestHelpDialog_SidebarSectionDocumentsOpen(t *testing.T) {
	if got := findHelpEntry(t, "Sidebar", "open calendar"); got != "enter" {
		t.Fatalf("sidebar open key = %q, want %q", got, "enter")
	}
}

func TestHelpDialog_RecentlyDeletedPopupDocumentsOpenAndRestore(t *testing.T) {
	if got := findHelpEntry(t, "Recently Deleted popup", "open"); got != "D · shift+d" {
		t.Fatalf("recently-deleted open key = %q, want %q", got, "D · shift+d")
	}
	if got := findHelpEntry(t, "Recently Deleted popup", "restore"); got != "r" {
		t.Fatalf("recently-deleted restore key = %q, want %q", got, "r")
	}
}

func findHelpEntry(t *testing.T, sectionTitle, desc string) string {
	t.Helper()
	for _, section := range NewHelpDialogModel(NewTheme(true)).sections() {
		if section.title != sectionTitle {
			continue
		}
		for _, entry := range section.entries {
			if entry.desc == desc {
				return entry.keys
			}
		}
		t.Fatalf("section %q missing entry %q", sectionTitle, desc)
	}
	t.Fatalf("missing section %q", sectionTitle)
	return ""
}
