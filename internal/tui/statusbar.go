package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func renderStatusBar(viewName string, width int) string {
	helpItems := []struct{ key, desc string }{
		{"hjkl", "navigate"},
		{"enter", "select"},
		{"n", "new"},
		{"1-4", "views"},
		{"tab", "sidebar"},
		{"g", "today"},
		{"?", "help"},
		{"q", "quit"},
	}

	var parts []string
	for _, h := range helpItems {
		parts = append(parts,
			helpKeyStyle.Render(h.key)+helpDescStyle.Render(" "+h.desc))
	}

	bar := strings.Join(parts, helpDescStyle.Render("  │  "))

	viewLabel := lipgloss.NewStyle().
		Bold(true).
		Foreground(DefaultTheme.Primary).
		Render("  tcal")

	left := viewLabel + "  " + statusBarStyle.Render(viewName)

	// Use available width
	style := lipgloss.NewStyle().
		Width(width).
		PaddingTop(1)

	return style.Render(left + "    " + bar)
}
