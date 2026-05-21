package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// paneTitle renders the detail pane heading: bold title on the first line,
// a faint horizontal rule on the second. Shared so both dialogs anchor the
// detail pane with the same visual weight. Both lines are padded to w
// cells so the result can be spliced into a row-zipped column without a
// trailing measurement pass.
func paneTitle(text string, w int) string {
	title := lipgloss.NewStyle().Bold(true).Render(truncateTo(text, w))
	title = padTrailing(title, w)
	rule := lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", w))
	return title + "\n" + rule
}

// padTrailing extends s with plain spaces so it is exactly w cells wide.
// If s is already w-wide (or wider) it is returned unchanged. Lipgloss
// styled strings with trailing SGR resets keep their resets before the
// spaces, so the padding is unstyled and matches the rendering lipgloss
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
// dialog. The list/detail split mirrors the dialog's golden-rectangle shape:
// details take the φ share (~61.8%) of the space left after the divider,
// the list takes the 1/φ share (~38.2%).
func listColumnWidth(innerW int) int {
	avail := max(innerW-dialogDividerWidth, 0)
	return max(min(avail*382/1000, innerW-24), 18)
}

// detailColumnWidth returns the width of the detail column given innerW,
// leaving space for the list column and divider.
func detailColumnWidth(innerW int) int {
	return max(innerW-listColumnWidth(innerW)-dialogDividerWidth, 10)
}
