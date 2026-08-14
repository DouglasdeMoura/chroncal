package tui

import "image/color"

// renderListRow truncates a pre-styled label to w cells. Selection/focus
// style is the caller's responsibility. The shell passes selection state
// only so callers that want a generic treatment can read it off their own
// models. Here it is unused. The returned string is *not* width-padded.
// padLines applies a single width pass to the whole column. A second
// Width(w) here would then double the (relatively expensive) lipgloss render.
func renderListRow(label string, w int, _, _ bool, _ color.Color) string {
	return truncateTo(label, max(w, 1))
}
