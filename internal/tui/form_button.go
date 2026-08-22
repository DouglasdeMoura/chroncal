package tui

import (
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
// background, only the label is bold red (Theme.Error). This matches
// Apple's iOS pattern of red text on a neutral pill rather than a
// flash of a red button.
//
// On focus, the two variants diverge deliberately. Normal flips to
// FormHighlight (the theme's selection accent). Danger inverts to a
// red pill with a contrast fg. That divergence costs "single focus
// signal" uniformity. It is the only way to keep destructive intent
// readable across themes whose FormHighlight lands in the warm/red
// end of the spectrum. That includes Dracula's pink, Osaka's orange,
// and Flexoki's coral. Red text on a warm highlight is unreadable.
// Put the red on the background. Compute a contrast label via
// oklch.ContrastingFg. That guarantees legibility on every theme. It
// emphasizes the destructive signal exactly when the user is about to
// commit it.
func DefaultButtonStyles() ButtonStyles {
	base := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
	t := activeTheme
	return ButtonStyles{
		Normal: ButtonStyle{
			Normal:  base.Background(t.ButtonBg).Foreground(oklch.ContrastingFg(t.ButtonBg)),
			Focused: base.Background(t.FormHighlight).Foreground(oklch.ContrastingFg(t.FormHighlight)),
		},
		Danger: ButtonStyle{
			Normal:  base.Background(t.ButtonBg).Foreground(t.Error).Bold(true),
			Focused: base.Background(t.Error).Foreground(oklch.ContrastingFg(t.Error)).Bold(true),
		},
	}
}

// ButtonAlign controls horizontal placement of the button row.
type ButtonAlign int

const (
	ButtonAlignRight  ButtonAlign = iota // default: right-aligned
	ButtonAlignCenter                    // centered (for dialogs)
	ButtonAlignLeft                      // left-aligned
)
