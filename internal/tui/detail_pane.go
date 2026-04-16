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
