// Package termcolor holds the ANSI SGR palette of the CLI text output and
// the helper that applies it. The compact tables of cmd/chroncal and the
// verbose event list of internal/tui use the same codes, so the two views
// show the same colors.
package termcolor

// SGR parameter codes of the palette.
const (
	// BoldCyan marks IDs, table headers, and date headings.
	BoldCyan = "1;36"
	// Bold marks a title.
	Bold = "1"
	// Dim marks dates, times, and descriptions.
	Dim = "2"
	// Yellow marks categories and times in the verbose view.
	Yellow = "33"
	// Magenta marks calendar names.
	Magenta = "35"
)

// Colorize wraps text in the SGR code and a reset when on is true. It
// returns text unchanged when on is false, when code is empty, or when text
// is empty.
func Colorize(text, code string, on bool) string {
	if !on || code == "" || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}
