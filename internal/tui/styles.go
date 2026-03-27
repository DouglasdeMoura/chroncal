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

// Calendar event colors — 8 distinct colors for multiple calendars.
var CalendarColors = []lipgloss.AdaptiveColor{
	{Light: "#7C3AED", Dark: "#A78BFA"}, // violet
	{Light: "#0284C7", Dark: "#38BDF8"}, // sky
	{Light: "#059669", Dark: "#34D399"}, // emerald
	{Light: "#D97706", Dark: "#FBBF24"}, // amber
	{Light: "#DC2626", Dark: "#F87171"}, // red
	{Light: "#DB2777", Dark: "#F472B6"}, // pink
	{Light: "#7C3AED", Dark: "#C084FC"}, // purple
	{Light: "#0891B2", Dark: "#22D3EE"}, // cyan
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(DefaultTheme.Primary)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(DefaultTheme.TextDim)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(DefaultTheme.Text).
			PaddingBottom(1)

	dayHeaderStyle = lipgloss.NewStyle().
			Foreground(DefaultTheme.TextDim).
			Width(4).
			Align(lipgloss.Center)

	dayCellStyle = lipgloss.NewStyle().
			Width(4).
			Align(lipgloss.Center)

	todayCellStyle = lipgloss.NewStyle().
			Width(4).
			Align(lipgloss.Center).
			Bold(true).
			Foreground(DefaultTheme.Today)

	selectedCellStyle = lipgloss.NewStyle().
				Width(4).
				Align(lipgloss.Center).
				Bold(true).
				Foreground(DefaultTheme.Primary).
				Background(DefaultTheme.Selected)

	outsideMonthStyle = lipgloss.NewStyle().
				Width(4).
				Align(lipgloss.Center).
				Foreground(DefaultTheme.Muted)

	eventDotStyle = lipgloss.NewStyle().
			Foreground(DefaultTheme.Primary)

	sidebarStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(DefaultTheme.Border).
			Padding(1, 2)

	panelStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(DefaultTheme.Border).
			Padding(1, 2)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(DefaultTheme.TextDim)

	helpKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(DefaultTheme.Primary)

	helpDescStyle = lipgloss.NewStyle().
			Foreground(DefaultTheme.TextDim)

	eventTimeStyle = lipgloss.NewStyle().
			Foreground(DefaultTheme.Secondary).
			Width(8)

	eventTitleStyle = lipgloss.NewStyle().
			Foreground(DefaultTheme.Text)

	eventDetailLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(DefaultTheme.TextDim).
				Width(12)

	eventDetailValueStyle = lipgloss.NewStyle().
				Foreground(DefaultTheme.Text)
)
