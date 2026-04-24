package tui

import (
	"image/color"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// badgeVariant controls the color scheme for a pill-style label.
type badgeVariant int

const (
	badgeNeutral badgeVariant = iota
	badgeOK
	badgeWarn
	badgeDanger
	badgeInfo
)

func badgeBackground(v badgeVariant) color.Color {
	switch v {
	case badgeOK:
		return activeTheme.BadgeOK
	case badgeWarn:
		return activeTheme.BadgeWarn
	case badgeDanger:
		return activeTheme.BadgeDanger
	case badgeInfo:
		return activeTheme.BadgeInfo
	default:
		return activeTheme.BadgeNeutral
	}
}

// badge renders a small pill with padded text and a colored background.
// Used for status labels, response indicators, and other short metadata
// that should read as a distinct token.
func badge(text string, v badgeVariant) string {
	return lipgloss.NewStyle().
		Background(badgeBackground(v)).
		Foreground(activeTheme.BadgeText).
		Padding(0, 1).
		Render(text)
}

// statusBadge maps a CalDAV/iCal status string to a colored pill. Unknown
// statuses fall back to the neutral variant so new values degrade
// gracefully.
func statusBadge(status string) string {
	if status == "" {
		return ""
	}
	label := titleCase(status)
	switch strings.ToUpper(status) {
	case "CONFIRMED":
		return badge(label, badgeOK)
	case "TENTATIVE":
		return badge(label, badgeWarn)
	case "CANCELLED", "CANCELED":
		return badge(label, badgeDanger)
	default:
		return badge(label, badgeNeutral)
	}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	return strings.ToUpper(lower[:1]) + lower[1:]
}
