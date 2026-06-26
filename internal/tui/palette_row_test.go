package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

// TestRenderPaletteRowShortcutNeverOverflows reproduces issue #353:
// the right-hand Shortcut segment was never truncated, so at minimum
// palette width (listW=26) an event date label (~25 cells) overflowed.
// The rendered row must never exceed the requested width.
func TestRenderPaletteRowShortcutNeverOverflows(t *testing.T) {
	cases := []struct {
		name     string
		width    int
		title    string
		shortcut string
	}{
		{
			// Minimum palette width (listW = max(boxW-4,10) where boxW=30).
			// "[Event] Jan 2 15:04-18:30" is 25 cells — wider than avail.
			name:     "event_date_at_minimum_width",
			width:    26,
			title:    "Team standup",
			shortcut: "[Event] Jan 2 15:04-18:30",
		},
		{
			// shortcut exactly fills the non-prefix space
			name:     "shortcut_fills_non_prefix",
			width:    26,
			title:    "X",
			shortcut: strings.Repeat("A", 24), // 24 cells = width - prefixW
		},
		{
			// shortcut wider than the whole row
			name:     "shortcut_wider_than_row",
			width:    26,
			title:    "Title",
			shortcut: strings.Repeat("B", 30),
		},
		{
			// Normal wide palette — no truncation should happen at all
			name:     "normal_width_no_overflow",
			width:    60,
			title:    "Team standup",
			shortcut: "[Event] Jan 2 15:04-18:30",
		},
		{
			// 1-cell shortcut (typical command palette entry) — must be unchanged
			name:     "single_char_shortcut",
			width:    26,
			title:    "Create event",
			shortcut: "t",
		},
		{
			// No shortcut at all
			name:     "no_shortcut",
			width:    26,
			title:    "Today",
			shortcut: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := PaletteCommand{
				Title:    tc.title,
				Shortcut: tc.shortcut,
			}
			got := renderPaletteRow(cmd, tc.width, false, Theme{})
			w := lipgloss.Width(got)
			if w > tc.width {
				t.Fatalf("renderPaletteRow width = %d, want ≤ %d\n  title=%q shortcut=%q row=%q",
					w, tc.width, tc.title, tc.shortcut, got)
			}
		})
	}
}
