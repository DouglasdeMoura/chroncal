package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// KeyAction names one TUI action that the [keys] table can remap. Defaults
// holds the built-in keys. The TUI uses them when the table does not name
// the action. Help is the short description that the TUI help text shows.
type KeyAction struct {
	Name     string
	Help     string
	Defaults []string
}

// keyActions lists the remappable actions of the TUI app keymap in the
// order of the documentation. It is the single source of truth: the TUI
// builds its app keymap from this list. The key strings use the Bubble Tea names
// (for example "ctrl+k", "tab", "shift+tab").
var keyActions = []KeyAction{
	{Name: "quit", Help: "quit", Defaults: []string{"q"}},
	{Name: "month_view", Help: "month", Defaults: []string{"m"}},
	{Name: "week_view", Help: "week", Defaults: []string{"w"}},
	{Name: "day_view", Help: "day", Defaults: []string{"d"}},
	{Name: "agenda_view", Help: "agenda", Defaults: []string{"a"}},
	{Name: "sidebar", Help: "sidebar", Defaults: []string{"\\"}},
	{Name: "week_numbers", Help: "week numbers", Defaults: []string{"#"}},
	{Name: "week_start", Help: "week start", Defaults: []string{"W"}},
	{Name: "create", Help: "new", Defaults: []string{"c"}},
	{Name: "switch_focus", Help: "switch focus", Defaults: []string{"tab", "shift+tab"}},
	{Name: "help", Help: "help", Defaults: []string{"?"}},
	{Name: "palette", Help: "commands", Defaults: []string{"/", "ctrl+k"}},
	{Name: "calendars", Help: "calendars", Defaults: []string{"C"}},
	{Name: "sync", Help: "sync", Defaults: []string{"s"}},
	{Name: "undo", Help: "undo", Defaults: []string{"u"}},
	{Name: "trash", Help: "recently deleted", Defaults: []string{"D"}},
}

// ReservedKeys names keys that a part of the TUI uses for its own work.
// Actions lists the app actions that the TUI checks before that part, so a
// key of such an action hides the key of the part. An empty Actions list
// means every action. Use names the part for the error text.
type ReservedKeys struct {
	Use     string
	Actions []string
	Keys    []string
}

// eventDialogKeys holds the keys of the day dialog and the event dialog.
// The two dialogs use the same keys.
var eventDialogKeys = []string{
	"up", "down", "left", "right", "h", "j", "k", "l",
	"tab", "shift+tab", "enter", "space", "esc", "q",
	"pgup", "pgdown", "ctrl+b", "ctrl+f", "home", "end",
	"e", "ctrl+d", "x", "delete", "y", "n", "m", "p",
}

// reservedKeys lists the keys that ValidateKeys rejects. It cannot read the
// TUI keymaps, because the TUI imports this package. The TUI test
// TestReservedKeysMatchTUIKeymaps compares each list with the keymaps, so
// the two cannot drift. A key that follows an app action (for example the
// create key in the agenda) is not in a list.
var reservedKeys = []ReservedKeys{
	{
		// interceptGlobalKeys checks these keys before every other key.
		Use:  "the TUI uses it to cancel and to quit",
		Keys: []string{"esc", "ctrl+c"},
	},
	{
		// handleKeyPress checks every app action before the view keys.
		Use: "the month, week, day, and agenda views use it",
		Keys: []string{
			"up", "down", "left", "right", "h", "j", "k", "l",
			"[", "]", "pgup", "pgdown", "t", "enter",
			"space", "e", "ctrl+d", "x", "delete", "o",
		},
	},
	{
		// handleKeyPress checks every app action before the sidebar keys.
		// Tab and shift+tab stay out: the switch_focus action owns them.
		Use: "the sidebar uses it",
		Keys: []string{
			"up", "down", "left", "right", "h", "j", "k", "l",
			"shift+up", "shift+down", "K", "J", "space", "enter",
			"[", "]", "t",
		},
	},
	{
		// The help key works over the trash view.
		Use:     "the trash view uses it",
		Actions: []string{"help"},
		Keys:    []string{"r", "x", "delete", "X", "space", "A"},
	},
	{
		// The help key works over the day dialog, and the day dialog
		// checks the create key before its own keys.
		Use:     "the day dialog uses it",
		Actions: []string{"help", "create"},
		Keys:    eventDialogKeys,
	},
	{
		// The help key works over the event dialog.
		Use:     "the event dialog uses it",
		Actions: []string{"help"},
		Keys:    eventDialogKeys,
	},
	{
		// The help dialog checks the help key before its own keys, so a
		// help key on a scroll key closes the dialog.
		Use:     "the help dialog uses it",
		Actions: []string{"help"},
		Keys: []string{
			"esc", "q", "up", "k", "down", "j",
			"pgup", "pgdown", "space", "home", "g", "end", "G",
		},
	},
	{
		// The calendar manager closes on the calendars key, and it
		// checks that key before its own keys.
		Use:     "the calendar manager uses it",
		Actions: []string{"calendars"},
		Keys: []string{
			"up", "down", "left", "right", "h", "j", "k", "l",
			"shift+up", "shift+down", "K", "J",
			"tab", "shift+tab", "enter", "space", "esc", "q", "a",
		},
	},
}

// ReservedKeyLists returns a copy of the keys that ValidateKeys rejects.
func ReservedKeyLists() []ReservedKeys {
	out := make([]ReservedKeys, len(reservedKeys))
	for i, r := range reservedKeys {
		out[i] = ReservedKeys{Use: r.Use, Actions: slices.Clone(r.Actions), Keys: slices.Clone(r.Keys)}
	}
	return out
}

// reservedKeyUse returns the Use text of the first list that reserves key k
// for the action. It returns "" when no list reserves k for the action.
func reservedKeyUse(action, k string) string {
	canon := CanonicalKey(k)
	for _, r := range reservedKeys {
		if len(r.Actions) > 0 && !slices.Contains(r.Actions, action) {
			continue
		}
		for _, rk := range r.Keys {
			if CanonicalKey(rk) == canon {
				return r.Use
			}
		}
	}
	return ""
}

// KeyActions returns a copy of the remappable TUI actions with their
// default keys and their help text.
func KeyActions() []KeyAction {
	out := make([]KeyAction, len(keyActions))
	for i, a := range keyActions {
		out[i] = KeyAction{Name: a.Name, Help: a.Help, Defaults: slices.Clone(a.Defaults)}
	}
	return out
}

// ResolveKeys returns the effective keys for each action. A key list in
// overrides replaces the defaults of that action. The function does not
// validate overrides. tui.Run calls ValidateKeys first.
func ResolveKeys(overrides map[string][]string) map[string][]string {
	out := make(map[string][]string, len(keyActions))
	for _, a := range keyActions {
		// A list with no usable key keeps the defaults. The TUI reads the
		// first key of each list, so an empty list must not reach it.
		if keys := normalizeKeyList(overrides[a.Name]); len(keys) > 0 {
			out[a.Name] = keys
			continue
		}
		out[a.Name] = slices.Clone(a.Defaults)
	}
	return out
}

// ValidateKeys reports the first problem in a [keys] table:
//   - an action name that the TUI does not know
//   - an empty key list or an empty key string
//   - a key that a view or a dialog uses (see reservedKeys)
//   - one key on two actions, after the overrides replace the defaults
func ValidateKeys(overrides map[string][]string) error {
	known := make(map[string]bool, len(keyActions))
	for _, a := range keyActions {
		known[a.Name] = true
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !known[name] {
			return fmt.Errorf("keys: unknown action %q (valid actions: %s)", name, strings.Join(keyActionNames(), ", "))
		}
		keys := overrides[name]
		if len(keys) == 0 {
			return fmt.Errorf("keys.%s: the key list is empty; set at least one key or remove the entry to use the default", name)
		}
		for _, k := range keys {
			k = strings.TrimSpace(k)
			if k == "" {
				return fmt.Errorf("keys.%s: a key is empty", name)
			}
			if lower, ok := keyCaseProblem(k); ok {
				return fmt.Errorf("keys.%s: %q never matches a key press; write %q", name, k, lower)
			}
			if use := reservedKeyUse(name, k); use != "" {
				return fmt.Errorf("keys.%s: chroncal reserves %q, because %s; choose another key", name, k, use)
			}
		}
	}

	owner := make(map[string]string)
	resolved := ResolveKeys(overrides)
	for _, a := range keyActions {
		for _, k := range resolved[a.Name] {
			canon := CanonicalKey(k)
			if prev, ok := owner[canon]; ok && prev != a.Name {
				return fmt.Errorf("keys: key %q is bound to both %s and %s", k, prev, a.Name)
			}
			owner[canon] = a.Name
		}
	}
	return nil
}

// keyCaseProblem reports a key name that Bubble Tea never gives because of
// its case. Bubble Tea names a key in lowercase ("ctrl+q", "tab"). An
// uppercase ASCII letter is valid only as a single letter ("W") or after
// "shift+" ("shift+W"). For a bad name it returns the lowercase form.
func keyCaseProblem(k string) (lower string, bad bool) {
	if c := CanonicalKey(k); len(c) == 1 {
		return "", false
	}
	b := []byte(k)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c - 'A' + 'a'
			bad = true
		}
	}
	return string(b), bad
}

// CanonicalKey returns one spelling for each name of a shifted letter.
// "shift+w", "shift+W", and "W" give "W". A terminal can report the key as
// "shift+w" or "W", and a user can write "shift+W" in the config file.
func CanonicalKey(k string) string {
	if rest, ok := strings.CutPrefix(k, "shift+"); ok && len(rest) == 1 {
		switch c := rest[0]; {
		case c >= 'a' && c <= 'z':
			return string(c - 'a' + 'A')
		case c >= 'A' && c <= 'Z':
			return rest
		}
	}
	return k
}

func keyActionNames() []string {
	names := make([]string, len(keyActions))
	for i, a := range keyActions {
		names[i] = a.Name
	}
	return names
}

// normalizeKeyList trims each key and removes repeated keys. The order of
// the first occurrence stays, because the help text shows the first key.
func normalizeKeyList(keys []string) []string {
	out := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[CanonicalKey(k)] {
			continue
		}
		seen[CanonicalKey(k)] = true
		out = append(out, k)
	}
	return out
}
