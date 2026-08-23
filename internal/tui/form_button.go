package tui

import (
	"image/color"
	"math"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

// ButtonVariant selects which style pair a button uses.
type ButtonVariant int

const (
	Button       ButtonVariant = iota // neutral default (submit / cancel / routine action)
	ButtonDanger                      // destructive action
)

// ButtonStyle holds the normal and focused styles for a button variant.
type ButtonStyle struct {
	Normal  lipgloss.Style
	Focused lipgloss.Style
}

// Render returns the styled button label.
func (bs ButtonStyle) Render(label string, focused bool) string {
	if focused {
		return bs.Focused.Render(label)
	}
	return bs.Normal.Render(label)
}

// ButtonStyles holds style pairs for every button variant.
type ButtonStyles struct {
	Normal ButtonStyle
	Danger ButtonStyle
}

// Get returns the ButtonStyle for the given variant.
func (bs ButtonStyles) Get(v ButtonVariant) ButtonStyle {
	if v == ButtonDanger {
		return bs.Danger
	}
	return bs.Normal
}

// DefaultButtonStyles returns button styles driven by the active theme.
//
// At rest, Danger is a quiet variant of Normal: same pill shape and
// background, only the label is bold red. On focus, the two variants
// diverge deliberately. Normal flips to FormHighlight (the theme's
// selection accent). Danger inverts to a red pill with a contrast fg.
// That divergence costs "single focus signal" uniformity. It is the only
// way to keep destructive intent readable across themes whose
// FormHighlight lands in the warm/red end of the spectrum. That includes
// Dracula's pink, Osaka's orange, and Flexoki's coral. Red text on a warm
// highlight is unreadable. Put the red on the background. Compute a
// contrast label via oklch.ContrastingFg. That guarantees legibility on
// every theme. It emphasizes the destructive signal exactly when the user
// is about to commit it.
func DefaultButtonStyles() ButtonStyles {
	base := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
	t := activeTheme
	return ButtonStyles{
		Normal: ButtonStyle{
			Normal:  base.Background(t.ButtonBg).Foreground(oklch.ContrastingFg(t.ButtonBg)),
			Focused: base.Background(t.FormHighlight).Foreground(oklch.ContrastingFg(t.FormHighlight)),
		},
		Danger: ButtonStyle{
			Normal:  base.Background(t.ButtonBg).Foreground(dangerRestingLabel(t.ButtonBg, t.Error)).Bold(true),
			Focused: base.Background(t.Error).Foreground(oklch.ContrastingFg(t.Error)).Bold(true),
		},
	}
}

// dangerRestingMinRatio is the contrast floor for the resting danger
// label. The label renders bold. WCAG treats bold text at this size as
// large text, so 3:1 is the floor.
const dangerRestingMinRatio = 3.0

// dangerRestingMinDeltaL is the smallest OKLCh lightness gap the label
// keeps from the pill. Red carries little WCAG luminance, so a ratio
// target alone cannot always separate the label from a mid-gray pill.
const dangerRestingMinDeltaL = 0.25

// dangerRestingLabel returns the resting label color for ButtonDanger.
// The pill stays Theme.ButtonBg; only the label moves. The label keeps
// the hue and chroma of Theme.Error, so it still reads red. The lightness
// moves away from the pill until the pair reaches dangerRestingMinRatio.
// The gap is at least dangerRestingMinDeltaL. Theme.Error itself wins
// when it already satisfies both. A saturated red cannot always reach
// 4.5:1 on a mid-gray pill; 3:1 for bold text plus the lightness gap
// keeps the label readable on every shipped theme.
func dangerRestingLabel(pill, err color.Color) color.Color {
	pillL, _, _, pillOK := oklch.FromColor(pill)
	errL, errC, errH, errOK := oklch.FromColor(err)
	if !pillOK || !errOK {
		return err
	}
	if oklch.ContrastRatio(err, pill) >= dangerRestingMinRatio &&
		math.Abs(errL-pillL) >= dangerRestingMinDeltaL {
		return err
	}
	const (
		hiL  = 0.90
		loL  = 0.15
		step = 0.01
	)
	if pillL < 0.55 {
		// Dark pill: push the label lighter.
		for l := math.Max(errL, pillL+dangerRestingMinDeltaL); l <= hiL; l += step {
			if c := oklch.ToColor(l, errC, errH); oklch.ContrastRatio(c, pill) >= dangerRestingMinRatio {
				return c
			}
		}
		return oklch.ToColor(hiL, errC, errH)
	}
	// Light pill: push the label darker.
	for l := math.Min(errL, pillL-dangerRestingMinDeltaL); l >= loL; l -= step {
		if c := oklch.ToColor(l, errC, errH); oklch.ContrastRatio(c, pill) >= dangerRestingMinRatio {
			return c
		}
	}
	return oklch.ToColor(loL, errC, errH)
}

// ButtonAlign controls horizontal placement of the button row.
type ButtonAlign int

const (
	ButtonAlignRight  ButtonAlign = iota // default: right-aligned
	ButtonAlignCenter                    // centered (for dialogs)
	ButtonAlignLeft                      // left-aligned
)
