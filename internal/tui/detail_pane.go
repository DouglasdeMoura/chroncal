package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// paneTitle renders the detail pane heading: the bold title on the first
// lines and a faint horizontal rule on the last line. Both dialogs use it,
// so the detail pane has the same visual weight in each. The caller wraps
// the title with wrapTitleLines. The function pads each line to w cells.
// The caller can then put the result into a row-zipped column with no
// other measurement.
func paneTitle(lines []string, w int) string {
	bold := lipgloss.NewStyle().Bold(true)
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		out = append(out, padTrailing(bold.Render(line), w))
	}
	out = append(out, lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", w)))
	return strings.Join(out, "\n")
}

// wrapTitleLines wraps a title into lines of at most w cells each, so the
// full title shows (issue #825). The width of each rune is its display
// width, so a wide (CJK) rune counts as two cells. The result has at most
// maxLines lines. When the title needs more lines, the last line gets the
// rest of the title, and truncateTo cuts it with "…".
//
// The rest comes from the title text, not from a join of the wrapped
// lines. wrapLine splits a long word (a URL, a CJK run) with no separator,
// so a join with " " puts a false space into the word.
func wrapTitleLines(text string, w, maxLines int) []string {
	lines := wrapLine(text, w)
	maxLines = max(maxLines, 1)
	if len(lines) <= maxLines {
		return lines
	}
	// wrapLine joins the words of strings.Fields with one space, so each
	// line is a substring of normalized, and one space or no separator
	// follows it.
	normalized := strings.Join(strings.Fields(text), " ")
	offset := 0
	for _, line := range lines[:maxLines-1] {
		offset += len(line)
		if offset < len(normalized) && normalized[offset] == ' ' {
			offset++
		}
	}
	return append(lines[:maxLines-1:maxLines-1], truncateTo(normalized[offset:], w))
}

// padTrailing extends s with plain spaces so it is exactly w cells wide.
// If s is already w-wide (or wider) it is returned unchanged. Lipgloss
// styled strings with SGR resets at the end keep their resets before the
// spaces. The padding is then unstyled and matches the render lipgloss
// would have produced.
func padTrailing(s string, w int) string {
	cw := lipgloss.Width(s)
	if cw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-cw)
}

// dialogDividerWidth is the cell width of the vertical divider between the
// list and detail panes in a two-column dialog.
const dialogDividerWidth = 3

// listColumnWidth returns the width of the list column for a two-column
// dialog. The list/detail split mirrors the dialog's golden-rectangle shape.
// Details take the φ share (~61.8%) of the space left after the divider.
// The list takes the 1/φ share (~38.2%).
func listColumnWidth(innerW int) int {
	avail := max(innerW-dialogDividerWidth, 0)
	return max(min(avail*382/1000, innerW-24), 18)
}

// detailColumnWidth returns the width of the detail column given innerW.
// It leaves space for the list column and divider.
func detailColumnWidth(innerW int) int {
	return max(innerW-listColumnWidth(innerW)-dialogDividerWidth, 10)
}
