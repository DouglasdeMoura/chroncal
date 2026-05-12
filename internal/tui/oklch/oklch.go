// Package oklch converts between sRGB and OKLCh and provides hue-stable
// color adjustments (dim, contrast) suitable for TUI styling.
//
// OKLCh is OKLab in polar form (L = lightness, C = chroma, H = hue). Because
// the space is perceptually uniform, scaling C or shifting L produces the
// visual change a user expects — unlike naive HSL/RGB math where identical
// numeric shifts look wildly different depending on hue.
//
// Background: https://evilmartians.com/chronicles/oklch-in-css-why-quit-rgb-hsl
// OKLab math: https://bottosson.github.io/posts/oklab/
package oklch

import (
	"fmt"
	"image/color"
	"math"

	lipgloss "charm.land/lipgloss/v2"
)

// FromRGB converts sRGB 8-bit channels to OKLCh.
func FromRGB(r, g, b uint8) (L, C, H float64) {
	lr := srgbToLinear(float64(r) / 255)
	lg := srgbToLinear(float64(g) / 255)
	lb := srgbToLinear(float64(b) / 255)
	LL, A, B := linearToOklab(lr, lg, lb)
	return LL, math.Hypot(A, B), math.Atan2(B, A)
}

// ToRGB converts OKLCh to sRGB 8-bit channels. If (L, C, H) lies outside the
// sRGB gamut, chroma is reduced while preserving L and H until the color
// fits — the hue-preserving approach recommended by CSS Color 4. If chroma
// reduction still doesn't fit, the achromatic L color is returned.
func ToRGB(L, C, H float64) (r, g, b uint8) {
	for range 16 {
		A := C * math.Cos(H)
		B := C * math.Sin(H)
		lr, lg, lb := oklabToLinear(L, A, B)
		if inGamut(lr) && inGamut(lg) && inGamut(lb) {
			return uint8(linearToSrgb(lr)*255 + 0.5),
				uint8(linearToSrgb(lg)*255 + 0.5),
				uint8(linearToSrgb(lb)*255 + 0.5)
		}
		C *= 0.85
	}
	s := uint8(linearToSrgb(clamp01(L))*255 + 0.5)
	return s, s, s
}

// FromColor resolves any color.Color (including lipgloss hex, ANSI 256, and
// named colors) to OKLCh. Returns ok=false for fully-transparent colors.
func FromColor(c color.Color) (L, C, H float64, ok bool) {
	r, g, b, a := c.RGBA()
	if a == 0 {
		return 0, 0, 0, false
	}
	L, C, H = FromRGB(uint8(r>>8), uint8(g>>8), uint8(b>>8))
	return L, C, H, true
}

// ToColor builds a lipgloss hex color from OKLCh, gamut-mapping as in ToRGB.
func ToColor(L, C, H float64) color.Color {
	r, g, b := ToRGB(L, C, H)
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))
}

// ShiftLightness returns c with its OKLCh L shifted by delta, clamped to
// [0, 1]. Hue and chroma are preserved. Positive delta makes the color
// lighter, negative makes it darker. Useful for deriving a "just
// noticeable" highlight from a background without guessing at the theme.
func ShiftLightness(c color.Color, delta float64) color.Color {
	L, C, H, ok := FromColor(c)
	if !ok {
		return c
	}
	L += delta
	if L < 0 {
		L = 0
	}
	if L > 1 {
		L = 1
	}
	return ToColor(L, C, H)
}

// Mix linearly interpolates between two colors in OKLab space.
// t ∈ [0,1]: 0 returns a, 1 returns b. Interpolation happens on the (L, a, b)
// axes (not (L, C, H)), so it stays perceptually uniform without the hue
// discontinuity you'd get from interpolating a polar coordinate.
//
// Useful for deriving readable mid-tones from foreground+background pairs:
// Mix(Text, Surface, 0.4) gives a "dim text" that tracks the actual palette
// instead of guessing at a single hex that works on every theme.
func Mix(a, b color.Color, t float64) color.Color {
	aL, aC, aH, ok1 := FromColor(a)
	bL, bC, bH, ok2 := FromColor(b)
	if !ok1 || !ok2 {
		return a
	}
	aA, aBB := aC*math.Cos(aH), aC*math.Sin(aH)
	bA, bBB := bC*math.Cos(bH), bC*math.Sin(bH)
	L := aL*(1-t) + bL*t
	A := aA*(1-t) + bA*t
	B := aBB*(1-t) + bBB*t
	C := math.Hypot(A, B)
	H := math.Atan2(B, A)
	return ToColor(L, C, H)
}

// Dim desaturates c and pulls its lightness toward mid (L=0.55) in OKLCh,
// keeping hue stable. factor ∈ [0,1]: 0 = unchanged, 1 = fully neutral gray.
func Dim(c color.Color, factor float64) color.Color {
	L, C, H, ok := FromColor(c)
	if !ok {
		return c
	}
	const targetL = 0.55
	L = L*(1-factor) + targetL*factor
	C *= 1 - factor
	return ToColor(L, C, H)
}

// ContrastingFg returns a foreground color with strong L contrast against bg
// while keeping bg's hue. Dark bg (L<0.55) → L=0.92; light bg → L=0.18, so
// the worst-case ΔL is ~0.37 — comfortably readable across all hues, since
// in OKLCh perceived contrast tracks ΔL independent of hue. Chroma is kept
// at 35% so the text reads as a tinted member of the calendar's color
// family rather than plain white/black. Falls back to white on a
// transparent bg.
func ContrastingFg(bg color.Color) color.Color {
	L, C, H, ok := FromColor(bg)
	if !ok {
		return lipgloss.Color("15")
	}
	if L < 0.55 {
		L = 0.92
	} else {
		L = 0.18
	}
	C *= 0.35
	return ToColor(L, C, H)
}

func srgbToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func linearToSrgb(v float64) float64 {
	if v <= 0.0031308 {
		return v * 12.92
	}
	return 1.055*math.Pow(v, 1.0/2.4) - 0.055
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

func inGamut(v float64) bool { return v >= -0.0001 && v <= 1.0001 }

func linearToOklab(r, g, b float64) (L, A, B float64) {
	l := 0.4122214708*r + 0.5363325363*g + 0.0514459929*b
	m := 0.2119034982*r + 0.6806995451*g + 0.1073969566*b
	s := 0.0883024619*r + 0.2817188376*g + 0.6299787005*b
	l_ := math.Cbrt(l)
	m_ := math.Cbrt(m)
	s_ := math.Cbrt(s)
	L = 0.2104542553*l_ + 0.7936177850*m_ - 0.0040720468*s_
	A = 1.9779984951*l_ - 2.4285922050*m_ + 0.4505937099*s_
	B = 0.0259040371*l_ + 0.7827717662*m_ - 0.8086757660*s_
	return
}

func oklabToLinear(L, A, B float64) (r, g, b float64) {
	l_ := L + 0.3963377774*A + 0.2158037573*B
	m_ := L - 0.1055613458*A - 0.0638541728*B
	s_ := L - 0.0894841775*A - 1.2914855480*B
	l := l_ * l_ * l_
	m := m_ * m_ * m_
	s := s_ * s_ * s_
	r = 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g = -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	b = -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	return
}
