package tui

import (
	"image/color"
	"math"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

// The resting Danger label must stay readable on its pill. On the default
// light theme, Theme.Error #DC2626 sat directly on the ANSI 240 pill
// (#585858): 1.47:1. The label keeps the error hue and chroma, but its
// lightness moves away from the pill until the pair reaches 3:1 (bold
// text) with at least 0.25 OKLCh L of separation.
func TestDangerRestingLabelReadable(t *testing.T) {
	// NOT t.Parallel(): DefaultButtonStyles reads the package-global
	// activeTheme, and this test swaps it.
	prev := ActiveTheme()
	t.Cleanup(func() { SetActiveTheme(prev) })

	for _, name := range BuiltinThemeNames() {
		for _, dark := range []bool{true, false} {
			th, err := LoadBuiltinTheme(name, dark)
			if err != nil {
				t.Fatalf("LoadBuiltinTheme(%q, dark=%v): %v", name, dark, err)
			}
			label := dangerRestingLabel(th.ButtonBg, th.Error)
			ratio := oklch.ContrastRatio(th.ButtonBg, label)
			if ratio < dangerRestingMinRatio {
				t.Errorf("%s dark=%v: resting danger pair reads %.2f:1, want >= %.1f:1",
					name, dark, ratio, dangerRestingMinRatio)
			}
			pillL, _, _, _ := oklch.FromColor(th.ButtonBg)
			labelL, _, _, _ := oklch.FromColor(label)
			if math.Abs(labelL-pillL) < dangerRestingMinDeltaL {
				t.Errorf("%s dark=%v: label L %.3f sits within %.2f of pill L %.3f",
					name, dark, labelL, dangerRestingMinDeltaL, pillL)
			}

			// The label must keep the error hue and enough chroma
			// to read as red, not as white or pink.
			_, _, errH, _ := oklch.FromColor(th.Error)
			_, labelC, labelH, _ := oklch.FromColor(label)
			if math.Abs(labelH-errH) > 0.05 {
				t.Errorf("%s dark=%v: label hue %.3f drifted from error hue %.3f",
					name, dark, labelH, errH)
			}
			// The gamut clamp reduces chroma at high lightness. Red at
			// L 0.75 carries about 0.13. The label must still hold
			// enough chroma to read as red, not as white or pink.
			if labelC < 0.08 {
				t.Errorf("%s dark=%v: label chroma %.3f is too low to read as red",
					name, dark, labelC)
			}
		}
	}
}

// DefaultButtonStyles must wire the clamped label into the resting
// Danger style. A readable helper that the style never uses fixes nothing.
func TestDefaultButtonStylesDangerRestingWired(t *testing.T) {
	// NOT t.Parallel(): SetActiveTheme writes the package-global theme.
	prev := ActiveTheme()
	t.Cleanup(func() { SetActiveTheme(prev) })

	th, err := LoadBuiltinTheme(DefaultThemeName, false)
	if err != nil {
		t.Fatalf("LoadBuiltinTheme: %v", err)
	}
	SetActiveTheme(th)

	styles := DefaultButtonStyles()
	bg := styles.Danger.Normal.GetBackground()
	fg := styles.Danger.Normal.GetForeground()
	if bg == nil || fg == nil {
		t.Fatal("resting Danger style lacks a background or foreground")
	}
	if ratio := oklch.ContrastRatio(bg, fg); ratio < dangerRestingMinRatio {
		t.Errorf("wired resting pair reads %.2f:1, want >= %.1f:1", ratio, dangerRestingMinRatio)
	}
}

// Theme.Error that already reads well passes through untouched. The clamp
// must not shift a good label.
func TestDangerRestingLabelKeepsGoodError(t *testing.T) {
	// White pill with a dark red: ratio 5.9:1, L gap 0.62. Keep it.
	pill := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	err := color.RGBA{R: 0x99, G: 0x22, B: 0x22, A: 0xff}
	if got := dangerRestingLabel(pill, err); got != color.Color(err) {
		t.Errorf("readable error was adjusted: got %v, want the original %v", got, err)
	}
}
