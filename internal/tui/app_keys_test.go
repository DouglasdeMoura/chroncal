package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
)

// remappedKeysModel returns a model with a [keys] table that moves the
// week view to "v", the help dialog to "H", the quit action to "x", and the
// create action to "n" (issue #797).
func remappedKeysModel() Model {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.viewMode = viewMonth
	return m.withKeys(newAppKeys(map[string][]string{
		"week_view": {"v"},
		"help":      {"H"},
		"quit":      {"x"},
		"create":    {"n"},
	}))
}

func TestDefaultAppKeysMatchBuiltInKeys(t *testing.T) {
	k := defaultAppKeys()
	for _, c := range []struct {
		name string
		b    key.Binding
		keys []string
	}{
		{"quit", k.Quit, []string{"q"}},
		{"week_start", k.WeekStart, []string{"W", "shift+w"}},
		{"palette", k.Palette, []string{"/", "ctrl+k"}},
		{"calendars", k.CalendarList, []string{"C", "shift+c"}},
		{"switch_focus", k.SwitchFocus, []string{"tab", "shift+tab"}},
		{"trash", k.TrashView, []string{"D", "shift+d"}},
	} {
		if got := c.b.Keys(); strings.Join(got, ",") != strings.Join(c.keys, ",") {
			t.Errorf("%s keys = %v, want %v", c.name, got, c.keys)
		}
		if got := c.b.Help().Key; got != c.keys[0] {
			t.Errorf("%s help key = %q, want %q", c.name, got, c.keys[0])
		}
	}
}

func TestRemappedKeyTriggersActionAndOldKeyDoesNot(t *testing.T) {
	m := remappedKeysModel()

	updated, _ := m.Update(keyPress("w"))
	if got := updated.(Model).viewMode; got != viewMonth {
		t.Fatalf("old key w switched the view to %v; want month", got)
	}

	updated, _ = m.Update(keyPress("v"))
	if got := updated.(Model).viewMode; got != viewWeek {
		t.Fatalf("remapped key v left the view at %v; want week", got)
	}
}

func TestRemappedQuitAndHelpKeys(t *testing.T) {
	m := remappedKeysModel()

	if _, _, handled := m.interceptGlobalKeys(keyPress("q")); handled {
		t.Fatal("old quit key q still opens the quit prompt")
	}
	if _, _, handled := m.interceptGlobalKeys(keyPress("x")); !handled {
		t.Fatal("remapped quit key x does not open the quit prompt")
	}
	if _, cmd, handled := m.interceptGlobalKeys(keyPress("?")); handled || cmd != nil {
		t.Fatal("old help key ? still opens the help dialog")
	}
	_, cmd, handled := m.interceptGlobalKeys(keyPress("H"))
	if !handled || cmd == nil {
		t.Fatal("remapped help key H does not open the help dialog")
	}
	if _, ok := cmd().(HelpDialogRequestedMsg); !ok {
		t.Fatalf("help key emitted %T, want HelpDialogRequestedMsg", cmd())
	}
}

func TestRemappedCreateKeyAppliesInAgenda(t *testing.T) {
	m := remappedKeysModel()
	if _, cmd := m.agenda.Update(keyPress("c")); cmd != nil {
		t.Fatalf("old create key c still emits %T in the agenda", cmd())
	}
	_, cmd := m.agenda.Update(keyPress("n"))
	if cmd == nil {
		t.Fatal("remapped create key n emits no command in the agenda")
	}
	if _, ok := cmd().(EventCreateMsg); !ok {
		t.Fatalf("create key emitted %T, want EventCreateMsg", cmd())
	}
}

func TestRemappedKeysShowInHelpFooterAndPalette(t *testing.T) {
	m := remappedKeysModel()

	updated, _ := m.Update(HelpDialogRequestedMsg{})
	help := updated.(Model).helpDialog
	for _, c := range []struct{ desc, want string }{
		{"this help", "H"},
		{"new event", "n"},
		{"quit", "x (main view)"},
		{"month / week / day / agenda view", "m · v · d · a"},
	} {
		if got := helpEntryKeys(t, help, c.desc); got != c.want {
			t.Errorf("help entry %q keys = %q, want %q", c.desc, got, c.want)
		}
	}

	footer := stripANSI(m.footer.Render(FooterMonth, 200, "", "", false, false))
	if !strings.Contains(footer, "H help") || !strings.Contains(footer, "n new") {
		t.Errorf("footer = %q, want the remapped H help and n new hints", footer)
	}
	if strings.Contains(footer, "? help") {
		t.Errorf("footer = %q still shows the old ? help hint", footer)
	}

	cmds := buildPaletteCommands(m)
	if got := paletteCommandByID(t, cmds, "view.week").Shortcut; got != "v" {
		t.Errorf("palette week shortcut = %q, want v", got)
	}
	if got := paletteCommandByID(t, cmds, "app.quit").Shortcut; got != "x" {
		t.Errorf("palette quit shortcut = %q, want x", got)
	}
}

func TestExpandKeyAliasesAddsShiftForm(t *testing.T) {
	got := expandKeyAliases([]string{"shift+g", "x", "G"})
	want := []string{"G", "shift+g", "x"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expandKeyAliases = %v, want %v", got, want)
	}
}

func helpEntryKeys(t *testing.T, h HelpDialogModel, desc string) string {
	t.Helper()
	for _, s := range h.sections() {
		for _, e := range s.entries {
			if e.desc == desc {
				return e.keys
			}
		}
	}
	t.Fatalf("help dialog has no entry %q", desc)
	return ""
}
