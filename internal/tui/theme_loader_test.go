package tui

import (
	"io"
	"os"
	"strconv"
	"testing"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
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
		if th.ButtonBg == nil {
			t.Errorf("dark=%v: ButtonBg is nil", dark)
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

func TestLoadSystemTheme(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th, err := LoadBuiltinTheme("system", dark)
		if err != nil {
			t.Fatalf("system (dark=%v): %v", dark, err)
		}
		if th.Primary == nil || th.BadgeOK == nil || th.FormHighlight == nil {
			t.Errorf("system (dark=%v): tokens unexpectedly nil", dark)
		}
		if len(th.CalendarSwatches) == 0 {
			t.Errorf("system (dark=%v): calendar swatches empty", dark)
		}
	}
}

func TestLoadThemeFallsBackOnUnknown(t *testing.T) {
	th := LoadTheme("does-not-exist", true)
	if th.Primary == nil {
		t.Fatal("fallback theme should still populate tokens")
	}
}

func TestLoadThemeEmptyNameIsDefault(t *testing.T) {
	th := LoadTheme("", true)
	if th.Primary == nil {
		t.Fatal("empty name should resolve to default")
	}
}

func TestResolveStringHonorsActivePalette(t *testing.T) {
	prev := ActivePalette()
	t.Cleanup(func() { SetActivePalette(prev) })

	var pal Palette
	pal[4] = lipgloss.Color("#123456")
	SetActivePalette(&pal)

	got := resolveString("4", true)
	r, g, b, _ := got.RGBA()
	if r>>8 != 0x12 || g>>8 != 0x34 || b>>8 != 0x56 {
		t.Errorf("expected palette[4] hex #123456, got rgb(%02x, %02x, %02x)", r>>8, g>>8, b>>8)
	}
	// The palette wins in both polarities.
	got = resolveString("4", false)
	r, g, b, _ = got.RGBA()
	if r>>8 != 0x12 || g>>8 != 0x34 || b>>8 != 0x56 {
		t.Errorf("light mode: expected palette[4] hex #123456, got rgb(%02x, %02x, %02x)", r>>8, g>>8, b>>8)
	}

	// Out-of-range ANSI indices fall through to lipgloss.
	if c := resolveString("240", true); c == nil {
		t.Errorf("ANSI 240 should fall through to lipgloss, not panic or return nil")
	}
	// Hex strings always fall through to lipgloss regardless of palette.
	hex := resolveString("#abcdef", true)
	hr, hg, hb, _ := hex.RGBA()
	if hr>>8 != 0xab || hg>>8 != 0xcd || hb>>8 != 0xef {
		t.Errorf("hex string should not be palette-translated, got rgb(%02x, %02x, %02x)", hr>>8, hg>>8, hb>>8)
	}
}

// A terminal that does not answer OSC 4 (older terminals, tmux without
// passthrough) must still get readable accent pairs. Index 8 used to fall
// through to lipgloss's VGA gray #808080. That gray sits at OKLCh L 0.60,
// so ContrastingFg picked a dark foreground while themed terminals render
// slot 8 on the dark side. The button pair then read 1.62:1.
func TestResolveStringWithoutPaletteKeepsContrastPolarity(t *testing.T) {
	prev := ActivePalette()
	t.Cleanup(func() { SetActivePalette(prev) })
	SetActivePalette(nil)

	t.Run("reported bug: dark-mode slot 8", func(t *testing.T) {
		bg := resolveString("8", true)
		r, g, b, _ := bg.RGBA()
		if r>>8 == 0x80 && g>>8 == 0x80 && b>>8 == 0x80 {
			t.Fatal("slot 8 resolved to the VGA default #808080; want an assumed dark Base16 hex")
		}
		bgL, _, _, ok := oklch.FromColor(bg)
		if !ok || bgL >= 0.55 {
			t.Fatalf("dark-mode slot 8 L=%.3f, want < 0.550 so the pill reads dark", bgL)
		}
		fg := oklch.ContrastingFg(bg)
		if ratio := oklch.ContrastRatio(bg, fg); ratio < 4.5 {
			t.Errorf("slot 8 pair reads %.2f:1, want >= 4.50:1", ratio)
		}
	})

	// Every ANSI reference a theme may carry, in both polarities. A slot
	// with a computed foreground must stay readable in degraded mode.
	for _, tc := range []struct {
		mode string
		dark bool
	}{
		{"dark", true},
		{"light", false},
	} {
		for idx := range assumedPaletteDark {
			bg := resolveString(strconv.Itoa(idx), tc.dark)
			fg := oklch.ContrastingFg(bg)
			if ratio := oklch.ContrastRatio(bg, fg); ratio < 4.5 {
				t.Errorf("%s mode: slot %d pair reads %.2f:1, want >= 4.50:1", tc.mode, idx, ratio)
			}
		}
	}

	t.Run("non-ANSI strings still fall through", func(t *testing.T) {
		if c := resolveString("4", true); c == nil {
			t.Fatal("nil palette must not turn ANSI 4 into a nil color")
		}
		if c := resolveString("240", true); c == nil {
			t.Error("ANSI 240 should fall through to lipgloss, not panic or return nil")
		}
	})
}

// The default theme's form_label must read on its surface in both
// polarities. A flat "240" (#585858) gave 6.8:1 on the light surface
// #F9FAFB but only 2.49:1 on the dark surface #111827.
func TestDefaultThemeFormLabelContrast(t *testing.T) {
	t.Parallel()

	cases := []struct {
		dark bool
		min  float64
	}{
		{false, 6.8}, // light keeps ANSI 240: 6.81:1 on #F9FAFB
		{true, 4.5},  // dark takes ANSI 245: 5.14:1 on #111827
	}
	for _, tc := range cases {
		th, err := LoadBuiltinTheme("default", tc.dark)
		if err != nil {
			t.Fatalf("LoadBuiltinTheme(default, dark=%v): %v", tc.dark, err)
		}
		if ratio := oklch.ContrastRatio(th.Surface, th.FormLabel); ratio < tc.min {
			t.Errorf("dark=%v: form_label reads %.2f:1 on the surface, want >= %.1f:1",
				tc.dark, ratio, tc.min)
		}
	}
}

func TestAutoSentinelDerivesFromTextAndSurface(t *testing.T) {
	// Force palette so "7" and "0" resolve to known hex values.
	prev := ActivePalette()
	t.Cleanup(func() { SetActivePalette(prev) })
	var pal Palette
	pal[0] = lipgloss.Color("#1D2021") // dark surface (gruvu-ish)
	pal[7] = lipgloss.Color("#D5C4A1") // light foreground
	SetActivePalette(&pal)

	th, err := LoadBuiltinTheme("system", true)
	if err != nil {
		t.Fatalf("LoadBuiltinTheme: %v", err)
	}
	if th.TextDim == nil {
		t.Fatal("auto sentinel did not populate TextDim")
	}
	if th.Muted == nil {
		t.Fatal("auto sentinel did not populate Muted")
	}
	// TextDim should land between Text and Surface on the L axis — meaning
	// it can't equal either endpoint.
	tr, tg, tb, _ := th.TextDim.RGBA()
	if (tr>>8 == 0x1D && tg>>8 == 0x20 && tb>>8 == 0x21) ||
		(tr>>8 == 0xD5 && tg>>8 == 0xC4 && tb>>8 == 0xA1) {
		t.Errorf("TextDim collapsed onto an endpoint: rgb(%02x, %02x, %02x)", tr>>8, tg>>8, tb>>8)
	}
	// Muted should be dimmer (i.e. closer to surface) than TextDim. We
	// can check that by comparing distance-to-surface in the green
	// channel as a stand-in (gruvu's bg green is 0x20, fg green is 0xC4
	// — a clean monotonic axis).
	_, mg, _, _ := th.Muted.RGBA()
	if mg>>8 >= tg>>8 {
		t.Errorf("Muted (g=%02x) should be closer to surface (g=0x20) than TextDim (g=%02x)", mg>>8, tg>>8)
	}
}

func TestAnsi16IndexBoundaries(t *testing.T) {
	cases := []struct {
		in     string
		idx    int
		wantOK bool
	}{
		{"0", 0, true},
		{"15", 15, true},
		{"16", 0, false},
		{"-1", 0, false},
		{"#abc", 0, false},
		{"", 0, false},
		{"abc", 0, false},
		{"999", 0, false},
	}
	for _, tc := range cases {
		idx, ok := ansi16Index(tc.in)
		if ok != tc.wantOK || (ok && idx != tc.idx) {
			t.Errorf("ansi16Index(%q) = (%d, %v); want (%d, %v)", tc.in, idx, ok, tc.idx, tc.wantOK)
		}
	}
}

// LoadTheme runs while the TUI owns the terminal. The fallback for an
// unknown theme must not write to stderr. A stderr write prints over the
// alternate screen. The warning goes to the state-dir log file instead
// (see config.SharedStateDirLogger).
func TestLoadThemeFallbackDoesNotWriteToStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	th := LoadTheme("does-not-exist", true)
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("LoadTheme wrote to stderr: %q", data)
	}
	if th.Primary == nil {
		t.Fatal("fallback theme should still populate tokens")
	}
}
