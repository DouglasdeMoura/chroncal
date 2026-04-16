package tui

import (
	"image/color"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// selectionPrefixWidth is the number of cells reserved at the left of each
// list row for the selection accent bar (one bar cell + one spacer).
const selectionPrefixWidth = 2

// renderListRow formats a single list row with a shared selection treatment:
// a left accent bar plus a subtle row tint when focused, a dim accent bar
// only when selected without focus, and a blank prefix otherwise.
//
// The label is truncated to w - selectionPrefixWidth cells so the combined
// prefix + content fits within w.
func renderListRow(label string, w int, selected, focused bool, tint color.Color) string {
	contentW := max(w-selectionPrefixWidth, 1)
	label = truncateTo(label, contentW)

	content := lipgloss.NewStyle().Width(contentW)
	if selected {
		content = content.Bold(true)
		if focused && tint != nil {
			content = content.Background(tint)
		}
	}

	var prefix string
	switch {
	case selected && focused:
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Render("❯") + " "
	case selected:
		prefix = lipgloss.NewStyle().Faint(true).Render("❯") + " "
	default:
		prefix = strings.Repeat(" ", selectionPrefixWidth)
	}

	return prefix + content.Render(label)
}
