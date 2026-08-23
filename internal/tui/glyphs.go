package tui

// Glyphs maps semantic names to display characters used across the TUI.
// Keep them here so it is easy to swap icon sets (e.g. Nerd Font
// vs plain Unicode) from a single place.
var Glyphs = map[string]string{
	// Focus / navigation
	"focus":    ">",

	// Checkbox
	"checkbox.on":  "[x]",
	"checkbox.off": "[ ]",

	// Status
	"status.ok":     "✓",

	// Select
	"select.prev": "◀",
	"select.next": "▶",

	// Shapes
	"dot": "●",

	// Time
	"time.arrow": "→",

	// Separators
	"separator.horizontal": "─",
	"separator.dot":        " · ",
}
