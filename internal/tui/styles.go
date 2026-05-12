package tui

import (
	"image/color"

	"charm.land/bubbles/v2/help"
	lipgloss "charm.land/lipgloss/v2"
)

// DefaultThemeName is the built-in theme loaded when nothing overrides it.
// "system" inherits the terminal's ANSI palette for chrome and the live
// terminal background for the selection highlight, so the TUI follows
// themed terminal setups (Omarchy, Catppuccin, Gruvbox, …) out of the box.
// The fixed-palette designer theme is still available as "default".
const DefaultThemeName = "system"

// Theme holds resolved colors for the current terminal background.
//
// Tokens are semantic, not presentational. Structural chrome (Primary,
// Border, Surface, …) lives alongside dedicated groups for badges, forms,
// and buttons so that every on-screen element can be recolored by swapping
// one theme instead of hunting down hardcoded values.
type Theme struct {
	// Structural chrome
	Primary   color.Color
	Secondary color.Color
	Accent    color.Color
	Muted     color.Color
	Text      color.Color
	TextDim   color.Color
	Border    color.Color
	Today     color.Color
	Selected  color.Color
	// SelectedText is the foreground color to use when painting text on
	// top of Selected. On themes where Selected and Text may converge
	// (e.g. Base16 system themes where both resolve to dark indices),
	// this token lets the theme break the tie explicitly.
	SelectedText color.Color
	Surface      color.Color
	Error        color.Color

	// Badge pills (BadgeText is the shared foreground).
	BadgeOK      color.Color
	BadgeWarn    color.Color
	BadgeDanger  color.Color
	BadgeInfo    color.Color
	BadgeNeutral color.Color
	BadgeText    color.Color

	// Form internals.
	FormLabel     color.Color
	FormRequired  color.Color
	FormError     color.Color
	FormHighlight color.Color // select flash + focused-button accent

	// Buttons (ButtonText is the shared foreground).
	ButtonPrimaryBg        color.Color
	ButtonPrimaryFocusedBg color.Color
	ButtonSecondaryBg      color.Color
	ButtonDangerBg         color.Color
	ButtonDangerFocusedBg  color.Color
	ButtonGhostFg          color.Color
	ButtonText             color.Color

	// Calendar color palette (hex swatches shown in the calendar dialog).
	CalendarSwatches []string
}

// activeTheme is the package-level theme consulted by helpers that can't
// easily receive a Theme through their call chain (badges, default form
// styles, per-field flash colors). The app installs the real theme via
// SetActiveTheme at boot and on background-change; tests inherit a sensible
// default from the init() below.
var activeTheme Theme

func init() {
	activeTheme = NewTheme(true)
}

// SetActiveTheme installs a theme for package-level helpers. Safe to call
// multiple times; the most recent call wins.
func SetActiveTheme(t Theme) { activeTheme = t }

// ActiveTheme returns the currently installed package-level theme.
func ActiveTheme() Theme { return activeTheme }

func newThemedHelp(theme Theme) help.Model {
	h := help.New()
	h.ShortSeparator = " · "
	h.Styles = help.Styles{
		ShortKey:       lipgloss.NewStyle().Foreground(theme.Text),
		ShortDesc:      lipgloss.NewStyle().Foreground(theme.TextDim),
		ShortSeparator: lipgloss.NewStyle().Foreground(theme.Muted),
		FullKey:        lipgloss.NewStyle().Foreground(theme.Text),
		FullDesc:       lipgloss.NewStyle().Foreground(theme.TextDim),
		FullSeparator:  lipgloss.NewStyle().Foreground(theme.Muted),
		Ellipsis:       lipgloss.NewStyle().Foreground(theme.Muted),
	}
	return h
}

// NewTheme returns a Theme with colors resolved for light or dark
// backgrounds. Delegates to the embedded built-in theme named by
// DefaultThemeName; any failure is a programming error because the file
// ships inside the binary.
func NewTheme(hasDarkBG bool) Theme {
	t, err := LoadBuiltinTheme(DefaultThemeName, hasDarkBG)
	if err != nil {
		panic("built-in default theme failed to load: " + err.Error())
	}
	return t
}
