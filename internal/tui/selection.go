package tui

import "image/color"

// renderListRow truncates a pre-styled label to w cells. Selection/focus
// styling is the caller's responsibility — the shell passes selection state
// only so callers that want a generic treatment can read it off their own
// models; here it is unused. The returned string is *not* width-padded:
// padLines applies a single width pass to the whole column, so re-applying
// Width(w) here would double the (relatively expensive) lipgloss render.
func renderListRow(label string, w int, _, _ bool, _ color.Color) string {
	return truncateTo(label, max(w, 1))
}
