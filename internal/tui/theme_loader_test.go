package tui

import (
	"testing"
)

func TestLoadBuiltinDefault(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th, err := LoadBuiltinTheme(DefaultThemeName, dark)
		if err != nil {
			t.Fatalf("LoadBuiltinTheme(default, dark=%v) failed: %v", dark, err)
		}
		if th.Primary == nil {
			t.Errorf("dark=%v: Primary is nil", dark)
		}
		if th.BadgeOK == nil {
			t.Errorf("dark=%v: BadgeOK is nil", dark)
		}
		if th.FormHighlight == nil {
			t.Errorf("dark=%v: FormHighlight is nil", dark)
		}
		if th.ButtonPrimaryBg == nil {
			t.Errorf("dark=%v: ButtonPrimaryBg is nil", dark)
		}
		if len(th.CalendarSwatches) == 0 {
			t.Errorf("dark=%v: CalendarSwatches is empty", dark)
		}
	}
}

func TestBuiltinThemeNamesIncludesDefault(t *testing.T) {
	names := BuiltinThemeNames()
	found := false
	for _, n := range names {
		if n == DefaultThemeName {
			found = true
		}
	}
	if !found {
		t.Fatalf("built-in names missing %q: %v", DefaultThemeName, names)
	}
}

func TestResolveColorShapes(t *testing.T) {
	t.Run("flat hex", func(t *testing.T) {
		c, err := resolveColor("#abc123", true, "f")
		if err != nil || c == nil {
			t.Fatalf("flat hex: got %v, %v", c, err)
		}
	})
	t.Run("flat ANSI", func(t *testing.T) {
		c, err := resolveColor("240", false, "f")
		if err != nil || c == nil {
			t.Fatalf("ANSI: got %v, %v", c, err)
		}
	})
	t.Run("variant dark picks dark", func(t *testing.T) {
		v := map[string]any{"light": "#111111", "dark": "#FFFFFF"}
		c, err := resolveColor(v, true, "f")
		if err != nil || c == nil {
			t.Fatalf("variant: got %v, %v", c, err)
		}
	})
	t.Run("variant light picks light", func(t *testing.T) {
		v := map[string]any{"light": "#111111", "dark": "#FFFFFF"}
		c, err := resolveColor(v, false, "f")
		if err != nil || c == nil {
			t.Fatalf("variant: got %v, %v", c, err)
		}
	})
	t.Run("missing is error", func(t *testing.T) {
		if _, err := resolveColor(nil, true, "f"); err == nil {
			t.Fatal("nil should error")
		}
	})
	t.Run("wrong type is error", func(t *testing.T) {
		if _, err := resolveColor(42, true, "f"); err == nil {
			t.Fatal("int should error")
		}
	})
	t.Run("variant missing key is error", func(t *testing.T) {
		v := map[string]any{"light": "#111"}
		if _, err := resolveColor(v, true, "f"); err == nil {
			t.Fatal("missing dark should error")
		}
	})
}

func TestLoadUnknownThemeReturnsError(t *testing.T) {
	if _, err := LoadBuiltinTheme("does-not-exist", true); err == nil {
		t.Fatal("expected error loading unknown theme")
	}
}
