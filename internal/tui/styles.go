package tui

import (
	"image/color"

	lipgloss "charm.land/lipgloss/v2"
)

// Theme holds resolved colors for the current terminal background.
type Theme struct {
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
	}
}
