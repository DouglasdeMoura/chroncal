package tui

// Glyphs maps semantic names to display characters used across the TUI.
// Centralising them here makes it easy to swap icon sets (e.g. Nerd Font
// vs plain Unicode) from a single place.
var Glyphs = map[string]string{
	// Focus / navigation
	"focus":    "\uf054", //  (chevron-right)
	"ellipsis": "…",

	// Checkbox
	"checkbox.on":  "\U000f0c52", // 󰱒
	"checkbox.off": "\U000f0131", // 󰄱

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
