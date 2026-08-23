package tui

import (
	"image/color"
	"reflect"
	"testing"
)

// TestBuiltinThemesResolveEveryToken guards the theme token registry. The
// Theme struct, the raw TOML mapping, the resolve picks, the fingerprint, and
// every themes/*.toml are hand-synced. A forgotten key or a missed wiring used
// to surface only as a silent runtime fallback to the default theme. This test
// fails instead: every color.Color field of both built-in themes must resolve,
// in dark and light variants.
func TestBuiltinThemesResolveEveryToken(t *testing.T) {
	colorType := reflect.TypeFor[color.Color]()
	for _, name := range BuiltinThemeNames() {
		for _, dark := range []bool{true, false} {
			theme, err := LoadBuiltinTheme(name, dark)
			if err != nil {
				t.Fatalf("LoadBuiltinTheme(%q, dark=%v): %v", name, dark, err)
			}
			v := reflect.ValueOf(theme)
			typ := v.Type()
			for i := 0; i < v.NumField(); i++ {
				field := typ.Field(i)
				if field.Type != colorType {
					continue
				}
				if v.Field(i).IsNil() {
					t.Errorf("%s (dark=%v): token %s did not resolve", name, dark, field.Name)
				}
			}
		}
	}
}
