package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"

	"github.com/douglasdemoura/chroncal/internal/config"
)

// remappedKeysModel returns a model with a [keys] table that moves the
// week view to "v", the help dialog to "H", the quit action to "x", the
// create action to "n", and the calendars action to "L" (issue #797).
func remappedKeysModel() Model {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.viewMode = viewMonth
	return m.withKeys(newAppKeys(map[string][]string{
		"week_view": {"v"},
		"help":      {"H"},
		"quit":      {"x"},
		"create":    {"n"},
		"calendars": {"L"},
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

// TestAppKeyMapCoversKeyActions makes sure that each config key action has
// a field in the app keymap and that each field gets a binding.
func TestAppKeyMapCoversKeyActions(t *testing.T) {
	var k appKeyMap
	fields := k.bindings()
	actions := config.KeyActions()
	if len(fields) != len(actions) {
		t.Fatalf("the app keymap maps %d actions, config has %d", len(fields), len(actions))
	}
	for _, a := range actions {
		if _, ok := fields[a.Name]; !ok {
			t.Errorf("the app keymap has no field for action %q", a.Name)
		}
	}
	keys := reflect.ValueOf(defaultAppKeys())
	for i := range keys.NumField() {
		b := keys.Field(i).Interface().(key.Binding)
		if len(b.Keys()) == 0 || b.Help().Desc == "" {
			t.Errorf("field %s has no keys or no help text", keys.Type().Field(i).Name)
		}
	}
}

func TestRemappedCreateKeyAppliesInDayDialog(t *testing.T) {
	m := remappedKeysModel()
	updated, _ := m.Update(CalendarDaySelectedMsg{Day: time.Date(2026, 4, 10, 0, 0, 0, 0, time.Local)})
	dialog := updated.(Model).dialog
	if _, cmd := dialog.Update(keyPress("c")); cmd != nil {
		t.Fatalf("old create key c still emits %T in the day dialog", cmd())
	}
	_, cmd := dialog.Update(keyPress("n"))
	if cmd == nil {
		t.Fatal("remapped create key n emits no command in the day dialog")
	}
	if _, ok := cmd().(EventCreateMsg); !ok {
		t.Fatalf("create key emitted %T, want EventCreateMsg", cmd())
	}
}

func TestRemappedCalendarsKeyClosesCalendarManager(t *testing.T) {
	m := remappedKeysModel()
	if _, cmd := m.calendarManager.Update(keyPress("C")); cmd != nil {
		if _, ok := cmd().(CalendarManagerClosedMsg); ok {
			t.Fatal("old calendars key C still closes the calendar manager")
		}
	}
	_, cmd := m.calendarManager.Update(keyPress("L"))
	if cmd == nil {
		t.Fatal("remapped calendars key L emits no command in the calendar manager")
	}
	if _, ok := cmd().(CalendarManagerClosedMsg); !ok {
		t.Fatalf("calendars key emitted %T, want CalendarManagerClosedMsg", cmd())
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
