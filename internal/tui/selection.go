package tui

import (
	"image/color"

	lipgloss "charm.land/lipgloss/v2"
)

// renderListRow clamps a pre-styled label to w cells. Selection/focus styling
// is the caller's responsibility — the shell passes selection state only so
// callers that want a generic treatment can read it off their own models;
// here it is unused.
func renderListRow(label string, w int, _, _ bool, _ color.Color) string {
	contentW := max(w, 1)
	label = truncateTo(label, contentW)
	return lipgloss.NewStyle().Width(contentW).Render(label)
}
