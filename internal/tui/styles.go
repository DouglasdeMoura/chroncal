package tui

import (
	"image/color"

	"charm.land/bubbles/v2/help"
	lipgloss "charm.land/lipgloss/v2"
)

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
	Surface   color.Color
	Error     color.Color

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

// NewTheme returns a Theme with colors resolved for light or dark backgrounds.
func NewTheme(hasDarkBG bool) Theme {
	ld := lipgloss.LightDark(hasDarkBG)
	return Theme{
		Primary:   ld(lipgloss.Color("#7C3AED"), lipgloss.Color("#A78BFA")),
		Secondary: ld(lipgloss.Color("#0284C7"), lipgloss.Color("#38BDF8")),
		Accent:    ld(lipgloss.Color("#059669"), lipgloss.Color("#34D399")),
		Muted:     ld(lipgloss.Color("#9CA3AF"), lipgloss.Color("#6B7280")),
		Text:      ld(lipgloss.Color("#1F2937"), lipgloss.Color("#F9FAFB")),
		TextDim:   ld(lipgloss.Color("#6B7280"), lipgloss.Color("#9CA3AF")),
		Border:    ld(lipgloss.Color("#D1D5DB"), lipgloss.Color("#374151")),
		Today:     ld(lipgloss.Color("#DC2626"), lipgloss.Color("#F87171")),
		Selected:  ld(lipgloss.Color("#EDE9FE"), lipgloss.Color("#312E81")),
		Surface:   ld(lipgloss.Color("#F9FAFB"), lipgloss.Color("#111827")),
		Error:     ld(lipgloss.Color("#DC2626"), lipgloss.Color("#F87171")),

		// Badges — flat ANSI 256 for stability against both backgrounds;
		// themes can override with brand colors.
		BadgeOK:      lipgloss.Color("28"),
		BadgeWarn:    lipgloss.Color("172"),
		BadgeDanger:  lipgloss.Color("124"),
		BadgeInfo:    lipgloss.Color("61"),
		BadgeNeutral: lipgloss.Color("240"),
		BadgeText:    lipgloss.Color("255"),

		// Form internals.
		FormLabel:     lipgloss.Color("240"),
		FormRequired:  lipgloss.Color("9"),
		FormError:     lipgloss.Color("9"),
		FormHighlight: lipgloss.Color("63"),

		// Buttons.
		ButtonPrimaryBg:        lipgloss.Color("61"),
		ButtonPrimaryFocusedBg: lipgloss.Color("63"),
		ButtonSecondaryBg:      lipgloss.Color("240"),
		ButtonDangerBg:         lipgloss.Color("52"),
		ButtonDangerFocusedBg:  lipgloss.Color("160"),
		ButtonGhostFg:          lipgloss.Color("240"),
		ButtonText:             lipgloss.Color("255"),

		// Calendar palette swatches.
		CalendarSwatches: []string{
			"#0074D9", "#7FDBFF", "#B10DC9",
			"#85144b", "#FF4136", "#FF851B",
			"#FFDC00", "#3D9970",
		},
	}
}
