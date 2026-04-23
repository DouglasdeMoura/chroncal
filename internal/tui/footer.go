package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// FooterContext names the high-level focus the app is showing so the footer
// can pick an appropriate key set. Callers map their focus/view state to
// one of these; the footer does not inspect app state on its own.
type FooterContext int

const (
	FooterMonthWeekDay FooterContext = iota
	FooterAgenda
	FooterAgendaEmpty
	FooterEventPopup
	FooterCalendarPopup
	FooterSidebar
)

// footerMinCols is the width below which the footer collapses to a minimal
// cue. Above it, full hint lists render (truncated if needed).
const footerMinCols = 40

// footerEllipsisCols is the width below which multi-hint lines lose their
// trailing items to an ellipsis. Above this and below the full width, a
// truncation is applied instead of collapsing.
const footerEllipsisCols = 60

// footerHint is a single key/description pair rendered in the footer.
type footerHint struct {
	keys string
	desc string
}

// FooterModel is a pure render surface: it holds no state beyond theme and
// the most recent context. The app passes context, width, and optional
// toast override each frame.
type FooterModel struct {
	theme Theme
}

// NewFooterModel returns a footer bound to the given theme.
func NewFooterModel(theme Theme) FooterModel {
	return FooterModel{theme: theme}
}

// SetTheme updates the theme for subsequent renders.
func (f *FooterModel) SetTheme(theme Theme) { f.theme = theme }

// Render composes one footer line for the given context and width. When
// toast is non-empty, it replaces the right-side hint half; syncStatus is
// rendered on the left. hasRSVP controls the optional RSVP keys on event
// popups, and showTodayHint controls whether the footer should advertise
// the `t today` shortcut when it is actionable in the active view.
//
// Below footerMinCols, the line collapses to "? help" + the single highest
// value local action (so the user always has a path forward).
// Between footerMinCols and footerEllipsisCols, the full hint list is
// truncated with an ellipsis.
// Above footerEllipsisCols, the full line renders.
func (f FooterModel) Render(ctx FooterContext, width int, syncStatus, toast string, hasRSVP, showTodayHint bool) string {
	if width <= 0 {
		return ""
	}

	prefixLabel := footerPrefix(ctx)
	hints := footerHints(ctx, hasRSVP, showTodayHint)

	// Collapse mode for very narrow terminals.
	if width < footerMinCols {
		return f.renderCollapsed(ctx, width, hints)
	}

	left := f.composeLeft(prefixLabel, syncStatus)
	right := toast
	if right == "" {
		// Between 40 and 60 cols, the hint list gets truncated with an
		// ellipsis. Label is lower-priority than actionable hints, so drop
		// it before truncation so commands survive longer.
		showLabel := prefixLabel != "" && width >= footerEllipsisCols
		label := ""
		if showLabel {
			label = prefixLabel
		}
		right = f.composeHints(label, hints)
	}

	return f.joinFooter(left, right, width)
}

// composeLeft produces the left half of the footer: a context label,
// separator, and optional sync status. When toast is absent the hint side
// also shows the prefix, so we omit it here to avoid duplicate labels.
func (f FooterModel) composeLeft(prefix, syncStatus string) string {
	if syncStatus == "" {
		return ""
	}
	return syncStatus
}

// composeHints renders "PREFIX · key desc · key desc · ?". The prefix is
// rendered in Muted so users visually register that the line is contextual.
func (f FooterModel) composeHints(prefix string, hints []footerHint) string {
	keyStyle := lipgloss.NewStyle().Foreground(f.theme.Text)
	descStyle := lipgloss.NewStyle().Foreground(f.theme.TextDim)
	sepStyle := lipgloss.NewStyle().Foreground(f.theme.Muted)
	labelStyle := lipgloss.NewStyle().Foreground(f.theme.Muted).Bold(true)

	sep := sepStyle.Render(" · ")
	parts := make([]string, 0, len(hints)+1)
	if prefix != "" {
		parts = append(parts, labelStyle.Render(prefix))
	}
	for _, h := range hints {
		parts = append(parts, keyStyle.Render(h.keys)+" "+descStyle.Render(h.desc))
	}
	return strings.Join(parts, sep)
}

// renderCollapsed keeps the line useful below footerMinCols by showing only
// a single high-value hint plus "?" as an escape hatch. The hint chosen is
// the first destructive-or-primary action for the context, with "? help"
// always rendered as a fallback.
func (f FooterModel) renderCollapsed(ctx FooterContext, width int, hints []footerHint) string {
	keyStyle := lipgloss.NewStyle().Foreground(f.theme.Text)
	descStyle := lipgloss.NewStyle().Foreground(f.theme.TextDim)
	sepStyle := lipgloss.NewStyle().Foreground(f.theme.Muted)

	top := collapsedTopHint(ctx)
	help := keyStyle.Render("?") + " " + descStyle.Render("help")
	if top.keys == "" {
		return help
	}
	line := keyStyle.Render(top.keys) + " " + descStyle.Render(top.desc) + sepStyle.Render(" · ") + help
	return truncateFooter(line, width)
}

// joinFooter splits left/right into a single line, applying ellipsis
// truncation when width is between footerMinCols and footerEllipsisCols.
func (f FooterModel) joinFooter(left, right string, width int) string {
	if width < footerEllipsisCols {
		// Narrow-but-not-collapsed: show the right side only, truncated with
		// ellipsis. Left (sync status) is dropped to preserve hint
		// discoverability.
		return truncateFooter(right, width)
	}
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	gap := width - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// RenderMinimal produces the reduced footer used while a dialog or palette
// is open: the dialog owns its own hint row, so the app footer degrades to
// sync status on the left and a toast (or the "? help" escape hatch) on
// the right. Pass showHelpHint=false when the help dialog itself is open
// (displaying "? help" while help is up is misleading).
func (f FooterModel) RenderMinimal(width int, syncStatus, toast string, showHelpHint bool) string {
	if width <= 0 {
		return ""
	}
	right := toast
	if right == "" && showHelpHint {
		keyStyle := lipgloss.NewStyle().Foreground(f.theme.Text)
		descStyle := lipgloss.NewStyle().Foreground(f.theme.TextDim)
		right = keyStyle.Render("?") + " " + descStyle.Render("help")
	}
	if width < footerEllipsisCols {
		return truncateFooter(right, width)
	}
	return f.joinFooter(syncStatus, right, width)
}

func footerPrefix(ctx FooterContext) string {
	switch ctx {
	case FooterMonthWeekDay:
		return "MONTH"
	case FooterAgenda, FooterAgendaEmpty:
		return "AGENDA"
	case FooterEventPopup:
		return "EVENT"
	case FooterCalendarPopup:
		return "CALENDARS"
	case FooterSidebar:
		return "SIDEBAR"
	}
	return ""
}

func footerHints(ctx FooterContext, hasRSVP, showTodayHint bool) []footerHint {
	switch ctx {
	case FooterMonthWeekDay:
		hints := []footerHint{
			{"↑↓←→", "move"},
			{"enter", "open"},
			{"c", "new"},
		}
		if showTodayHint {
			hints = append(hints, footerHint{"t", "today"})
		}
		return append(hints, footerHint{"?", "help"})
	case FooterAgenda:
		return []footerHint{
			{"↑↓", "move"},
			{"enter", "open"},
			{"x", "delete"},
			{"c", "new"},
			{"?", "help"},
		}
	case FooterAgendaEmpty:
		hints := []footerHint{
			{"c", "create event"},
		}
		if showTodayHint {
			hints = append(hints, footerHint{"t", "today"})
		}
		return append(hints, footerHint{"?", "help"})
	case FooterEventPopup:
		h := []footerHint{
			{"e", "edit"},
			{"x", "delete"},
			{"←→", "prev/next"},
			{"esc", "close"},
		}
		if hasRSVP {
			h = append(h, footerHint{"y/n/m", "RSVP"})
		}
		return h
	case FooterCalendarPopup:
		return []footerHint{
			{"a", "add"},
			{"e", "edit"},
			{"x", "delete"},
			{"space", "toggle"},
			{"esc", "close"},
		}
	case FooterSidebar:
		return []footerHint{
			{"↑↓", "move"},
			{"space", "toggle"},
			{"\\", "collapse"},
			{"tab", "back"},
		}
	}
	return nil
}

// collapsedTopHint returns the single most important hint for the given
// context when the footer has to collapse to near-nothing.
func collapsedTopHint(ctx FooterContext) footerHint {
	switch ctx {
	case FooterMonthWeekDay:
		return footerHint{"c", "new"}
	case FooterAgenda:
		return footerHint{"x", "delete"}
	case FooterAgendaEmpty:
		return footerHint{"c", "create"}
	case FooterEventPopup:
		return footerHint{"x", "delete"}
	case FooterCalendarPopup:
		return footerHint{"a", "add"}
	case FooterSidebar:
		return footerHint{"space", "toggle"}
	}
	return footerHint{}
}

func truncateFooter(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return ""
	}
	// Lipgloss handles ANSI-aware width; trim one rune at a time from the
	// rendered form. This isn't the fastest path but fires rarely — only
	// when the terminal is under 60 columns.
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > width-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
