package model

import (
	"fmt"
	"regexp"
	"strings"
)

// hexColorPattern matches the calendar color form that the CLI, the TUI,
// the config file, and the database use: "#" and six hex digits.
var hexColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// ValidateHexColor checks a calendar color. The CLI --color flag and the
// config [[calendars]] entries share it. The package sits below config and
// calendar, so both can import it without a cycle.
func ValidateHexColor(color string) error {
	if !hexColorPattern.MatchString(strings.TrimSpace(color)) {
		return fmt.Errorf("color %q must be a hex color such as #0F766E", color)
	}
	return nil
}

// NormalizeHexColor returns the color in the stored form: trimmed and in
// upper case, so two spellings of one color compare equal.
func NormalizeHexColor(color string) string {
	return strings.ToUpper(strings.TrimSpace(color))
}
