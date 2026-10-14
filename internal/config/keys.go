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
// validate overrides. Load calls ValidateKeys first.
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
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("keys.%s: a key is empty", name)
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

// CanonicalKey returns one spelling for the two Bubble Tea names of a
// shifted letter. "shift+w" and "W" give the same result, because a
// terminal can report the key in either form.
func CanonicalKey(k string) string {
	if rest, ok := strings.CutPrefix(k, "shift+"); ok && len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z' {
		return strings.ToUpper(rest)
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
