package tui

import (
	"image/color"

	lipgloss "charm.land/lipgloss/v2"
)

// button renders a standard dialog action button. Pass underlineIndex < 0 to
// skip the keyboard-shortcut underline.
func button(text string, underlineIndex int, focused bool) string {
	return buttonStyled(text, underlineIndex, focused, false)
}

// buttonStyled renders a dialog action button with optional primary emphasis.
// Focused beats primary for background color so the keyboard highlight is
// always visible, while primary keeps its bold weight.
func buttonStyled(text string, underlineIndex int, focused, primary bool) string {
	var bg color.Color
	switch {
	case focused:
		bg = lipgloss.Color("63")
	case primary:
		bg = lipgloss.Color("61")
	default:
		bg = lipgloss.Color("240")
	}
	style := lipgloss.NewStyle().
		Background(bg).
		Foreground(lipgloss.Color("255"))
	if primary {
		style = style.Bold(true)
	}

	rendered := style.Padding(0, 1).Render(text)
	if underlineIndex >= 0 && underlineIndex < len(text) {
		rendered = lipgloss.StyleRanges(rendered,
			lipgloss.NewRange(1+underlineIndex, 1+underlineIndex+1, style.Underline(true)))
	}
	return rendered
}

// buttonDanger renders a destructive-action button. Unfocused uses a muted
// red-tinted background so the action is visible but doesn't compete with the
// primary button. Focused uses a bright red so the keyboard highlight reads
// clearly as "you're about to delete something."
func buttonDanger(text string, underlineIndex int, focused bool) string {
	var bg color.Color
	if focused {
		bg = lipgloss.Color("160")
	} else {
		bg = lipgloss.Color("52")
	}
	style := lipgloss.NewStyle().
		Background(bg).
		Foreground(lipgloss.Color("255"))

	rendered := style.Padding(0, 1).Render(text)
	if underlineIndex >= 0 && underlineIndex < len(text) {
		rendered = lipgloss.StyleRanges(rendered,
			lipgloss.NewRange(1+underlineIndex, 1+underlineIndex+1, style.Underline(true)))
	}
	return rendered
}
