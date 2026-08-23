package oklch

import (
	"fmt"
	"image/color"
	"math"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

func hexColor(s string) color.RGBA {
	var r, g, b uint8
	fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
	return color.RGBA{R: r, G: g, B: b, A: 0xff}
}

func hexString(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// FromRGB followed by ToRGB must reproduce an in-gamut sRGB byte exactly.
// Theme contrast math and fallback derivation both depend on that lossless
// round trip.
func TestFromRGBToRGBRoundTrip(t *testing.T) {
	t.Parallel()

	colors := []string{
		"#000000", // black
		"#ffffff", // white
		"#ff0000", // pure red
		"#00ff00", // pure green
		"#0000ff", // pure blue
		"#767676", // the lightest gray that passes 4.5:1 on white
		"#123456", // arbitrary dark blue
		"#7c3aed", // default light theme primary
		"#a78bfa", // default dark theme primary
		"#dc2626", // default light theme error
		"#111827", // default dark theme surface
		"#585858", // xterm 240, the default theme button pill
	}
	for _, hex := range colors {
		c := hexColor(hex)
		L, C, H := FromRGB(c.R, c.G, c.B)
		r, g, b := ToRGB(L, C, H)
		if r != c.R || g != c.G || b != c.B {
			t.Errorf("round trip %s = #%02x%02x%02x; want the same bytes back", hex, r, g, b)
		}
	}
}

func TestToRGBGamutClamp(t *testing.T) {
	t.Parallel()

	_, greenC, greenH := FromRGB(0x00, 0xff, 0x00)

	t.Run("in-gamut green stays exact", func(t *testing.T) {
		greenL, _, _ := FromRGB(0x00, 0xff, 0x00)
		r, g, b := ToRGB(greenL, greenC, greenH)
		if r != 0 || g != 0xff || b != 0 {
			t.Errorf("in-gamut pure green clamped: got #%02x%02x%02x", r, g, b)
		}
	})

	t.Run("out-of-gamut chroma reduces, L and H stay", func(t *testing.T) {
		r, g, b := ToRGB(0.8, 0.4, greenH)
		L, C, H := FromRGB(r, g, b)
		if math.Abs(L-0.8) > 0.01 {
			t.Errorf("lightness drifted: got L=%.3f, want 0.800", L)
		}
		if math.Abs(H-greenH) > 0.01 {
			t.Errorf("hue drifted: got H=%.3f, want %.3f", H, greenH)
		}
		if C >= 0.4 {
			t.Errorf("chroma not reduced: got C=%.3f, want < 0.400", C)
		}
	})

	t.Run("impossible L falls back to achromatic", func(t *testing.T) {
		// L=1 admits only white. Chroma reduction can not fit any
		// chroma, so the documented achromatic fallback must kick in.
		r, g, b := ToRGB(1.0, 0.5, greenH)
		if r != 0xff || g != 0xff || b != 0xff {
			t.Errorf("achromatic fallback: got #%02x%02x%02x, want #ffffff", r, g, b)
		}
	})
}

// ContrastingFg must flip polarity around L=0.55 and always land far from
// the background lightness. Buttons, badges, and chips draw their
// foreground from it, so a same-side result is unreadable.
func TestContrastingFgPolarity(t *testing.T) {
	t.Parallel()

	t.Run("dark backgrounds get a light foreground", func(t *testing.T) {
		for _, hex := range []string{"#000000", "#111827", "#1e293b", "#44475a", "#585858"} {
			bg := hexColor(hex)
			fg := ContrastingFg(bg)
			fgL, _, _, ok := FromColor(fg)
			if !ok {
				t.Fatalf("%s: foreground not opaque: %v", hex, fg)
			}
			if fgL < 0.9 {
				t.Errorf("%s: foreground L=%.3f, want >= 0.900", hex, fgL)
			}
			if ratio := ContrastRatio(bg, fg); ratio < 4.5 {
				t.Errorf("%s: ratio %.2f, want >= 4.50", hex, ratio)
			}
		}
	})

	t.Run("light backgrounds get a dark foreground", func(t *testing.T) {
		for _, hex := range []string{"#ffffff", "#f9fafb", "#ede9fe", "#cfcfde", "#b8b8b8"} {
			bg := hexColor(hex)
			fg := ContrastingFg(bg)
			fgL, _, _, ok := FromColor(fg)
			if !ok {
				t.Fatalf("%s: foreground not opaque: %v", hex, fg)
			}
			if fgL > 0.25 {
				t.Errorf("%s: foreground L=%.3f, want <= 0.250", hex, fgL)
			}
			if ratio := ContrastRatio(bg, fg); ratio < 4.5 {
				t.Errorf("%s: ratio %.2f, want >= 4.50", hex, ratio)
			}
		}
	})

	t.Run("boundary red keeps the documented worst case", func(t *testing.T) {
		// #dc2626 sits just above L=0.55. It must take the dark
		// foreground. The worst-case delta-L of 0.37 gives about 3.9:1
		// for red because red carries little WCAG luminance.
		bg := hexColor("#dc2626")
		fg := ContrastingFg(bg)
		fgL, _, _, _ := FromColor(fg)
		if fgL > 0.25 {
			t.Errorf("foreground L=%.3f, want <= 0.250", fgL)
		}
		if ratio := ContrastRatio(bg, fg); ratio < 3.5 {
			t.Errorf("ratio %.2f, want >= 3.50", ratio)
		}
	})

	t.Run("hue stays with the background", func(t *testing.T) {
		bg := hexColor("#7c3aed")
		_, _, bgH, _ := FromColor(bg)
		_, _, fgH, _ := FromColor(ContrastingFg(bg))
		if math.Abs(fgH-bgH) > 0.05 {
			t.Errorf("foreground hue %.3f drifted from background hue %.3f", fgH, bgH)
		}
	})

	t.Run("transparent background falls back to white", func(t *testing.T) {
		fg := ContrastingFg(color.RGBA{})
		if hexString(fg) != "#ffffff" {
			t.Errorf("transparent bg: got %s, want #ffffff", hexString(fg))
		}
	})
}

func TestContrastRatioKnownPairs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b string
		want float64
	}{
		{"#000000", "#ffffff", 21.0},
		{"#ffffff", "#000000", 21.0}, // order independent
		{"#ffffff", "#ffffff", 1.0},
		{"#767676", "#ffffff", 4.54}, // the lightest gray that passes 4.5:1 on white
		{"#585858", "#f9fafb", 6.81}, // default light theme: form label on surface
		{"#585858", "#111827", 2.49}, // default dark theme: the failure G4 fixes
	}
	for _, tc := range cases {
		got := ContrastRatio(hexColor(tc.a), hexColor(tc.b))
		if math.Abs(got-tc.want) > 0.01 {
			t.Errorf("ContrastRatio(%s, %s) = %.4f, want %.2f", tc.a, tc.b, got, tc.want)
		}
	}
	if got := ContrastRatio(nil, hexColor("#ffffff")); got != 21.0 {
		t.Errorf("ContrastRatio(nil, white) = %.4f, want 21.00 (nil counts as black)", got)
	}
}

func TestMixEndpoints(t *testing.T) {
	t.Parallel()

	a := hexColor("#dc2626")
	b := hexColor("#f9fafb")
	if got := hexString(Mix(a, b, 0)); got != "#dc2626" {
		t.Errorf("Mix(a, b, 0) = %s, want a (#dc2626)", got)
	}
	if got := hexString(Mix(a, b, 1)); got != "#f9fafb" {
		t.Errorf("Mix(a, b, 1) = %s, want b (#f9fafb)", got)
	}
}

func TestShiftLightnessClamps(t *testing.T) {
	t.Parallel()

	c := hexColor("#585858")
	if got := hexString(ShiftLightness(c, 10)); got != "#ffffff" {
		t.Errorf("overshoot up: got %s, want #ffffff", got)
	}
	if got := hexString(ShiftLightness(c, -10)); got != "#000000" {
		t.Errorf("overshoot down: got %s, want #000000", got)
	}
	// A transparent color passes through untouched.
	transparent := color.RGBA{}
	if got := ShiftLightness(transparent, 0.2); got != color.Color(transparent) {
		t.Errorf("transparent input must pass through, got %v", got)
	}
}

func TestFromColorRejectsTransparent(t *testing.T) {
	t.Parallel()

	if _, _, _, ok := FromColor(color.RGBA{}); ok {
		t.Error("FromColor(transparent) reported ok, want ok=false")
	}
	if _, _, _, ok := FromColor(lipgloss.Color("#abc123")); !ok {
		t.Error("FromColor(hex) reported not ok, want ok=true")
	}
}
