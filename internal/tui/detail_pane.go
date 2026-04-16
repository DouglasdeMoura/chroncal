package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// paneTitle renders the detail pane heading: bold title on the first line,
// a faint horizontal rule on the second. Shared so both dialogs anchor the
// detail pane with the same visual weight.
func paneTitle(text string, w int) string {
	title := lipgloss.NewStyle().Bold(true).Render(truncateTo(text, w))
	rule := lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", w))
	return title + "\n" + rule
}

// actionBar anchors a row of buttons to the bottom of a detail pane with a
// faint separator rule above, visually grouping the actions as a distinct
// region rather than floating buttons in whitespace.
func actionBar(buttons string, w int) string {
	rule := lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", w))
	return rule + "\n" + buttons
}

// dialogDividerWidth is the cell width of the vertical divider between the
// list and detail panes in a two-column dialog.
const dialogDividerWidth = 3

// listColumnWidth returns the width of the list column for a two-column
// dialog, targeting ~35% of innerW with sensible floor and headroom so the
// detail pane always has room for labeled fields.
func listColumnWidth(innerW int) int {
	return max(min(innerW*35/100, innerW-24), 18)
}

// detailColumnWidth returns the width of the detail column given innerW,
// leaving space for the list column and divider.
func detailColumnWidth(innerW int) int {
	return max(innerW-listColumnWidth(innerW)-dialogDividerWidth, 10)
}
