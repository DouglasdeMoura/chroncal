package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"

	"github.com/douglasdemoura/chroncal/internal/config"
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

// newAppKeys builds the app keymap from the [keys] table of config.toml
// (issue #797). An action without an entry keeps its default keys. The
// help text of a binding shows its first key. The caller validates
// overrides with config.ValidateKeys before the call.
func newAppKeys(overrides map[string][]string) appKeyMap {
	keys := config.ResolveKeys(overrides)
	bind := func(action, desc string) key.Binding {
		list := keys[action]
		return key.NewBinding(key.WithKeys(expandKeyAliases(list)...), key.WithHelp(list[0], desc))
	}
	return appKeyMap{
		Quit:         bind("quit", "quit"),
		MonthView:    bind("month_view", "month"),
		WeekView:     bind("week_view", "week"),
		DayView:      bind("day_view", "day"),
		AgendaView:   bind("agenda_view", "agenda"),
		Sidebar:      bind("sidebar", "sidebar"),
		WeekNumbers:  bind("week_numbers", "week numbers"),
		WeekStart:    bind("week_start", "week start"),
		Create:       bind("create", "new"),
		SwitchFocus:  bind("switch_focus", "switch focus"),
		Help:         bind("help", "help"),
		Palette:      bind("palette", "commands"),
		CalendarList: bind("calendars", "calendars"),
		Sync:         bind("sync", "sync"),
		Undo:         bind("undo", "undo"),
		TrashView:    bind("trash", "recently deleted"),
	}
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
			add("shift+" + strings.ToLower(canon))
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
