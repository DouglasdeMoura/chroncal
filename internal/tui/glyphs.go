package tui

// Glyphs maps semantic names to display characters used across the TUI.
// Centralising them here makes it easy to swap icon sets (e.g. Nerd Font
// vs plain Unicode) from a single place.
var Glyphs = map[string]string{
	// Focus / navigation
	"focus":    ">",
	"ellipsis": "…",

	// Checkbox
	"checkbox.on":  "[x]",
	"checkbox.off": "[ ]",

	// Status
	"status.ok":     "✓",
	"status.danger": "✗",

	// Shapes
	"dot": "●",

	// Separators
	"separator.vertical":   "│",
	"separator.horizontal": "─",
	"separator.dot":        " · ",
}
