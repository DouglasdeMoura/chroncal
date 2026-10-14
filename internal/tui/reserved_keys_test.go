package tui

import (
	"reflect"
	"slices"
	"testing"

	"charm.land/bubbles/v2/key"

	"github.com/douglasdemoura/chroncal/internal/config"
)

// bindingKeys returns the canonical keys of every key.Binding field of a
// keymap struct. skip names the fields that follow an app action. Those
// fields carry an app key, so the reserved lists leave them out.
func bindingKeys(t *testing.T, keymap any, skip ...string) []string {
	t.Helper()
	v := reflect.ValueOf(keymap)
	var out []string
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if slices.Contains(skip, f.Name) {
			continue
		}
		b, ok := v.Field(i).Interface().(key.Binding)
		if !ok {
			t.Fatalf("%T.%s is not a key.Binding", keymap, f.Name)
		}
		for _, k := range b.Keys() {
			out = append(out, config.CanonicalKey(k))
		}
	}
	return out
}

func canonicalSet(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, k := range l {
			out = append(out, config.CanonicalKey(k))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestReservedKeysMatchTUIKeymaps compares each reserved list in config
// with the keymaps of the part of the TUI that the list names. A new key in
// a view must also go into the reserved list, or a remapped app action
// hides it in silence.
func TestReservedKeysMatchTUIKeymaps(t *testing.T) {
	sk := defaultListDialogKeys()
	shellKeys := bindingKeys(t, sk)
	// The calendar manager closes on esc, q, and the calendars keys. The
	// calendars keys follow the app action, so they are not reserved.
	managerKeys := defaultCalendarManagerKeys()
	managerKeys.Close = key.NewBinding(key.WithKeys("esc", "q"))

	want := map[string][]string{
		"the TUI uses it to cancel and to quit": {"esc", "ctrl+c"},
		"the month, week, day, and agenda views use it": canonicalSet(
			bindingKeys(t, defaultCalendarKeys()),
			bindingKeys(t, defaultWeekKeys()),
			bindingKeys(t, defaultDayKeys()),
			bindingKeys(t, defaultAgendaKeys(), "Create"),
		),
		// The sidebar keys of the switch_focus action follow the app
		// action, so they are not reserved.
		"the sidebar uses it": canonicalSet(
			bindingKeys(t, defaultCalendarListKeys(), "Tab", "ShiftTab"),
			bindingKeys(t, defaultMiniMonthKeys()),
		),
		"the trash view uses it": canonicalSet(
			bindingKeys(t, defaultTrashKeys()),
		),
		"the day dialog uses it": canonicalSet(
			shellKeys,
			bindingKeys(t, defaultEventDialogKeys(), "Create"),
		),
		"the event dialog uses it": canonicalSet(
			bindingKeys(t, defaultEventViewKeys()),
		),
		"the calendar manager uses it": canonicalSet(
			bindingKeys(t, managerKeys),
			bindingKeys(t, defaultCalendarListKeys()),
		),
	}
	lists := config.ReservedKeyLists()
	if len(lists) != len(want) {
		t.Fatalf("config has %d reserved lists, want %d", len(lists), len(want))
	}
	for _, r := range lists {
		w, ok := want[r.Use]
		if !ok {
			t.Errorf("reserved list %q has no keymap check", r.Use)
			continue
		}
		if got := canonicalSet(r.Keys); !slices.Equal(got, canonicalSet(w)) {
			t.Errorf("reserved keys for %q = %v, want the keymap keys %v", r.Use, got, canonicalSet(w))
		}
	}
}
