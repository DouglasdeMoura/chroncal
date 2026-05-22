package tui

import "testing"

func TestHelpDialog_EventsSectionDocumentsAgendaToggleEmpty(t *testing.T) {
	if got := findHelpEntry(t, "Events", "toggle empty days (agenda)"); got != "o" {
		t.Fatalf("agenda toggle-empty key = %q, want %q", got, "o")
	}
}

func TestHelpDialog_EventsSectionDocumentsEditAndDuplicate(t *testing.T) {
	if got := findHelpEntry(t, "Events", "edit"); got != "e" {
		t.Fatalf("edit key = %q, want %q", got, "e")
	}
	if got := findHelpEntry(t, "Events", "duplicate"); got != "ctrl+d" {
		t.Fatalf("duplicate key = %q, want %q", got, "ctrl+d")
	}
}

func TestHelpDialog_WindowsSectionDocumentsRecentlyDeleted(t *testing.T) {
	if got := findHelpEntry(t, "Windows", "recently deleted"); got != "D · shift+d" {
		t.Fatalf("recently-deleted key = %q, want %q", got, "D · shift+d")
	}
}

func TestHelpDialog_CalendarsSectionDocumentsSetDefault(t *testing.T) {
	if got := findHelpEntry(t, "Calendars", "set as default"); got != "*" {
		t.Fatalf("set-default key = %q, want %q", got, "*")
	}
}

func TestHelpDialog_CalendarsSectionDocumentsSidebarOpen(t *testing.T) {
	if got := findHelpEntry(t, "Calendars", "open calendar"); got != "enter (sidebar)" {
		t.Fatalf("sidebar open key = %q, want %q", got, "enter (sidebar)")
	}
}

func TestHelpDialog_WindowsSectionDocumentsTrashRestore(t *testing.T) {
	if got := findHelpEntry(t, "Windows", "restore"); got != "r (trash)" {
		t.Fatalf("trash restore key = %q, want %q", got, "r (trash)")
	}
}

func TestHelpDialog_TopLevelSectionsAreTaskShaped(t *testing.T) {
	// The Apple-style layout collapses what used to be 11 widget-shaped
	// buckets into a small fixed list of task-shaped ones. Locking the
	// order down so future additions stay inside an existing group
	// instead of sprouting a new section for every new dialog. Command
	// Palette is its own section — it has palette-specific keys
	// (ctrl+k/j navigation, pgup/pgdn) that don't apply elsewhere.
	want := []string{"Getting Around", "Events", "Calendars", "Command Palette", "Windows"}
	sections := NewHelpDialogModel(NewTheme(true)).sections()
	if got := len(sections); got != len(want) {
		t.Fatalf("section count = %d, want %d", got, len(want))
	}
	for i, w := range want {
		if sections[i].title != w {
			t.Errorf("section[%d] = %q, want %q", i, sections[i].title, w)
		}
	}
}

func TestHelpDialog_CommandPaletteSectionDocumentsOpen(t *testing.T) {
	if got := findHelpEntry(t, "Command Palette", "open"); got != "/ · ctrl+k" {
		t.Fatalf("palette open key = %q, want %q", got, "/ · ctrl+k")
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
