package tui

import (
	"image/color"

	lipgloss "charm.land/lipgloss/v2"
)

// renderListRow formats a single list row with a shared selection treatment:
// bold label when selected, plus a subtle row tint when focused.
func renderListRow(label string, w int, selected, focused bool, tint color.Color) string {
	contentW := max(w, 1)
	label = truncateTo(label, contentW)

	content := lipgloss.NewStyle().Width(contentW)
	if selected {
		content = content.Bold(true)
		if focused && tint != nil {
			content = content.Background(tint)
		}
	}

	return content.Render(label)
}
