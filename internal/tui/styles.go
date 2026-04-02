package tui

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Primary   lipgloss.AdaptiveColor
	Secondary lipgloss.AdaptiveColor
	Accent    lipgloss.AdaptiveColor
	Muted     lipgloss.AdaptiveColor
	Text      lipgloss.AdaptiveColor
	TextDim   lipgloss.AdaptiveColor
	Border    lipgloss.AdaptiveColor
	Today     lipgloss.AdaptiveColor
	Selected  lipgloss.AdaptiveColor
	Surface   lipgloss.AdaptiveColor
	Error     lipgloss.AdaptiveColor
}

var DefaultTheme = Theme{
	Primary:   lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"},
	Secondary: lipgloss.AdaptiveColor{Light: "#0284C7", Dark: "#38BDF8"},
	Accent:    lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"},
	Muted:     lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#6B7280"},
	Text:      lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#F9FAFB"},
	TextDim:   lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"},
	Border:    lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#374151"},
	Today:     lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#F87171"},
	Selected:  lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#312E81"},
	Surface:   lipgloss.AdaptiveColor{Light: "#F9FAFB", Dark: "#111827"},
	Error:     lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#F87171"},
}
