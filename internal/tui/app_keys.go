package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"

	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/event"
)

type appKeyMap struct {
	Quit         key.Binding
	MonthView    key.Binding
	WeekView     key.Binding
	DayView      key.Binding
	AgendaView   key.Binding
	Sidebar      key.Binding
	WeekNumbers  key.Binding
	WeekStart    key.Binding
	Create       key.Binding
	SwitchFocus  key.Binding
	Help         key.Binding
	Palette      key.Binding
	CalendarList key.Binding
	Sync         key.Binding
	Undo         key.Binding
	TrashView    key.Binding
}

func defaultAppKeys() appKeyMap {
	return newAppKeys(nil)
}

// bindings maps each config action name to its field in the keymap.
// TestAppKeyMapCoversKeyActions makes sure that this table and
// config.KeyActions name the same actions.
func (k *appKeyMap) bindings() map[string]*key.Binding {
	return map[string]*key.Binding{
		"quit":         &k.Quit,
		"month_view":   &k.MonthView,
		"week_view":    &k.WeekView,
		"day_view":     &k.DayView,
		"agenda_view":  &k.AgendaView,
		"sidebar":      &k.Sidebar,
		"week_numbers": &k.WeekNumbers,
		"week_start":   &k.WeekStart,
		"create":       &k.Create,
		"switch_focus": &k.SwitchFocus,
		"help":         &k.Help,
		"palette":      &k.Palette,
		"calendars":    &k.CalendarList,
		"sync":         &k.Sync,
		"undo":         &k.Undo,
		"trash":        &k.TrashView,
	}
}

// newAppKeys builds the app keymap from the [keys] table of config.toml
// (issue #797). An action without an entry keeps its default keys. The
// help text of a binding shows its first key and the help text of the
// config action. The first key shows in its canonical form ("W", not
// "shift+w"), as the help dialog shows it. The caller validates overrides
// with config.ValidateKeys before the call.
func newAppKeys(overrides map[string][]string) appKeyMap {
	keys := config.ResolveKeys(overrides)
	var k appKeyMap
	fields := k.bindings()
	for _, a := range config.KeyActions() {
		field, ok := fields[a.Name]
		if !ok {
			panic("tui: the app keymap has no field for the config key action " + a.Name)
		}
		list := keys[a.Name]
		*field = key.NewBinding(key.WithKeys(expandKeyAliases(list)...), key.WithHelp(config.CanonicalKey(list[0]), a.Help))
	}
	return k
}

// withKeys installs the app keymap. It also gives the keymap to the
// footer and to the child models that use an app key. A remap in
// config.toml then applies in those children, and the old key stops
// there too. The help dialog and the palette read m.keys directly.
func (m Model) withKeys(keys appKeyMap) Model {
	m.keys = keys
	m.footer.SetKeys(keys)
	m.agenda = m.agenda.WithAppKeys(keys)
	m.calendarManager = m.calendarManager.WithAppKeys(keys)
	return m
}

// newDayDialog builds the day dialog (the events of one day) with the
// app keymap, the theme, and the screen size of the model.
func (m Model) newDayDialog(day time.Time, events []event.Event) EventDialogModel {
	return NewEventDialogModel(day, events, m.calendars, newThemedHelp(m.theme)).
		WithAppKeys(m.keys).
		SetSelectedColor(m.theme.Selected).
		SetSize(m.width, m.height)
}

// expandKeyAliases adds the second Bubble Tea name of each shifted letter.
// A terminal reports shift+w as "W" or as "shift+w". The binding must
// match the two forms.
func expandKeyAliases(keys []string) []string {
	out := make([]string, 0, len(keys)*2)
	seen := make(map[string]bool, len(keys)*2)
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, k := range keys {
		canon := config.CanonicalKey(k)
		if len(canon) == 1 && canon[0] >= 'A' && canon[0] <= 'Z' {
			add(canon)
			add("shift+" + string(canon[0]-'A'+'a'))
			continue
		}
		add(k)
	}
	return out
}

// keyLabel returns the first key of a binding, for a footer hint or a
// palette shortcut.
func keyLabel(b key.Binding) string {
	return b.Help().Key
}

// keysLabel joins the keys of a binding for the help dialog. It leaves
// out the "shift+x" alias of an uppercase letter "X", because the
// uppercase letter already names the key.
func keysLabel(b key.Binding) string {
	keys := b.Keys()
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if config.CanonicalKey(k) != k {
			continue
		}
		parts = append(parts, k)
	}
	return strings.Join(parts, " · ")
}

// isShiftedKey reports whether a key name holds the shift modifier:
// "shift+..." or an uppercase letter (the shifted form of a letter key).
func isShiftedKey(k string) bool {
	canon := config.CanonicalKey(k)
	return strings.HasPrefix(canon, "shift+") || (len(canon) == 1 && canon[0] >= 'A' && canon[0] <= 'Z')
}

// isSidebarStepKey reports whether the sidebar uses a key to move between
// its stops: tab and shift+tab.
func isSidebarStepKey(k string) bool {
	return k == "tab" || k == "shift+tab"
}
