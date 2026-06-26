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
	FooterMonth FooterContext = iota
	FooterWeek
	FooterDay
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
		return f.renderCollapsed(ctx, width)
	}

	// Between 40 and 60 cols, the hint list gets truncated with an ellipsis.
	// Label is lower-priority than actionable hints, so drop it before
	// truncation so commands survive longer.
	showLabel := prefixLabel != "" && width >= footerEllipsisCols
	label := ""
	if showLabel {
		label = prefixLabel
	}

	left := f.composeLeft(label, syncStatus)
	right := toast
	if right == "" {
		right = f.composeHints(hints)
	}

	return f.joinFooter(left, right, width)
}

// composeLeft produces the left half of the footer: a context label followed
// by an optional sync status. Either (or both) may be empty. The label is
// marked as clickable so the app can cycle views when the user clicks it.
func (f FooterModel) composeLeft(prefix, syncStatus string) string {
	labelStyle := lipgloss.NewStyle().Foreground(f.theme.Muted).Bold(true)
	sepStyle := lipgloss.NewStyle().Foreground(f.theme.Muted)

	parts := make([]string, 0, 2)
	if prefix != "" {
		parts = append(parts, mouseMark("footer:label", labelStyle.Render(prefix)))
	}
	if syncStatus != "" {
		parts = append(parts, syncStatus)
	}
	return strings.Join(parts, sepStyle.Render(" · "))
}

// composeHints renders "key desc · key desc · ?".
func (f FooterModel) composeHints(hints []footerHint) string {
	keyStyle := lipgloss.NewStyle().Foreground(f.theme.Text)
	descStyle := lipgloss.NewStyle().Foreground(f.theme.TextDim)
	sepStyle := lipgloss.NewStyle().Foreground(f.theme.Muted)

	sep := sepStyle.Render(" · ")
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, keyStyle.Render(h.keys)+" "+descStyle.Render(h.desc))
	}
	return strings.Join(parts, sep)
}

// renderCollapsed keeps the line useful below footerMinCols by showing only
// a single high-value hint plus "?" as an escape hatch. The hint chosen is
// the first destructive-or-primary action for the context, with "? help"
// always rendered as a fallback.
func (f FooterModel) renderCollapsed(ctx FooterContext, width int) string {
	keyStyle := lipgloss.NewStyle().Foreground(f.theme.Text)
	descStyle := lipgloss.NewStyle().Foreground(f.theme.TextDim)
	sepStyle := lipgloss.NewStyle().Foreground(f.theme.Muted)

	top := collapsedTopHint(ctx)
	help := keyStyle.Render("?") + " " + descStyle.Render("help")
	if top.keys == "" {
		return help
	}
	line := keyStyle.Render(top.keys) + " " + descStyle.Render(top.desc) + sepStyle.Render(" · ") + help
	return truncateTo(line, width)
}

// joinFooter splits left/right into a single line, applying ellipsis
// truncation when width is between footerMinCols and footerEllipsisCols.
func (f FooterModel) joinFooter(left, right string, width int) string {
	if width < footerEllipsisCols {
		// Narrow-but-not-collapsed: show the right side only, truncated with
		// ellipsis. Left (sync status) is dropped to preserve hint
		// discoverability.
		return truncateTo(right, width)
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
		return truncateTo(right, width)
	}
	return f.joinFooter(syncStatus, right, width)
}

func footerPrefix(ctx FooterContext) string {
	switch ctx {
	case FooterMonth:
		return "MONTH"
	case FooterWeek:
		return "WEEK"
	case FooterDay:
		return "DAY"
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
	case FooterMonth, FooterWeek, FooterDay:
		hints := []footerHint{
			{"↑↓←→", "move"},
			{"[]", bracketDesc(ctx)},
			{"enter", "open"},
			{"c", "new"},
		}
		if showTodayHint {
			hints = append(hints, footerHint{"t", "today"})
		}
		return append(hints, footerHint{"D", "recently deleted"}, footerHint{"?", "help"})
	case FooterAgenda:
		hints := []footerHint{
			{"↑↓", "move"},
			{"[]", "month"},
			{"enter", "open"},
			{"e", "edit"},
			{"ctrl+d", "duplicate"},
			{"x", "delete"},
			{"c", "new"},
			{"o", "empty days"},
		}
		if showTodayHint {
			hints = append(hints, footerHint{"t", "today"})
		}
		return append(hints, footerHint{"D", "recently deleted"}, footerHint{"?", "help"})
	case FooterAgendaEmpty:
		hints := []footerHint{
			{"c", "create event"},
			{"o", "empty days"},
		}
		if showTodayHint {
			hints = append(hints, footerHint{"t", "today"})
		}
		return append(hints, footerHint{"D", "recently deleted"}, footerHint{"?", "help"})
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

// bracketDesc returns the description for the "[]" bracket-navigation hint
// in views where it jumps by the view's natural unit.
func bracketDesc(ctx FooterContext) string {
	switch ctx {
	case FooterMonth:
		return "month"
	case FooterWeek:
		return "week"
	case FooterDay:
		return "day"
	default:
		// Other contexts don't use bracket navigation.
		return ""
	}
}

// collapsedTopHint returns the single most important hint for the given
// context when the footer has to collapse to near-nothing.
func collapsedTopHint(ctx FooterContext) footerHint {
	switch ctx {
	case FooterMonth, FooterWeek, FooterDay:
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
