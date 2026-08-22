package tui

import (
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
)

type DayOptions struct {
	Day          time.Time
	Events       []CalendarEvent
	Today        time.Time
	Width        int
	Height       int
	ShowHeader   bool
	ScrollOffset int
	LinesPerHour int
}

func DayGrid(opts DayOptions) string {
	if opts.Width <= 0 || opts.Height <= 0 {
		return ""
	}

	lph := opts.LinesPerHour
	if lph < 1 {
		lph = defaultLinesPerHour
	}
	totalRows := totalHours * lph
	day := opts.Day

	colWidth := max(opts.Width-timeLabelWidth-2, 1)

	placed := placeDayEvents(opts.Events, day, lph)
	resolveOverlaps(placed)

	allDayRows := max(dayAllDayCount(opts.Events, day), 1)

	headerLines := 0
	if opts.ShowHeader {
		headerLines = 2
	}
	fixedLines := headerLines + 1 + allDayRows + 1
	viewportHeight := max(opts.Height-fixedLines, 1)

	maxScroll := max(totalRows-viewportHeight, 0)
	scrollOffset := min(max(opts.ScrollOffset, 0), maxScroll)

	showBottomRule := scrollOffset+viewportHeight >= totalRows
	if showBottomRule && viewportHeight > 1 {
		viewportHeight--
	}

	now := time.Now().Local()
	nowRow := now.Hour()*lph + now.Minute()*lph/60
	nowTimeLabel := now.Format("15:04")
	isToday := day.Format("2006-01-02") == now.Format("2006-01-02")

	faint := lipgloss.NewStyle().Faint(true)
	faintSep := faint.Render("│")
	nowStyle := lipgloss.NewStyle().Foreground(ActiveTheme().Today).Bold(true)
	nowSep := nowStyle.Render("│")

	var out strings.Builder

	if opts.ShowHeader {
		title := day.Format("Monday, January 2, 2006")
		out.WriteString(lipgloss.NewStyle().Bold(true).Width(opts.Width).Align(lipgloss.Left).Render(title))
		out.WriteString("\n\n")
	}

	out.WriteString(renderDayHRule(colWidth, "┌", "", true))
	out.WriteString("\n")

	out.WriteString(renderDayAllDayRows(opts.Events, day, colWidth, allDayRows))

	out.WriteString(renderDayHRule(colWidth, "├", "╮", true))
	out.WriteString("\n")

	for row := scrollOffset; row < scrollOffset+viewportHeight && row < totalRows; row++ {
		if row > scrollOffset {
			out.WriteString("\n")
		}

		isNowRow := isToday && row == nowRow
		out.WriteString(renderTimeLabel(row, lph, isNowRow, nowTimeLabel))

		if isNowRow {
			out.WriteString(nowSep)
		} else {
			out.WriteString(faintSep)
		}

		matches := findPlacedEvents(placed, row, 0)
		if len(matches) > 0 {
			out.WriteString(renderOverlappingCells(matches, row, colWidth))
		} else if isNowRow {
			out.WriteString(nowStyle.Render(strings.Repeat("─", colWidth)))
		} else {
			out.WriteString(strings.Repeat(" ", colWidth))
		}

		if isNowRow {
			out.WriteString(nowSep)
		} else {
			out.WriteString(faintSep)
		}
	}

	if showBottomRule {
		out.WriteString("\n")
		out.WriteString(renderDayHRule(colWidth, "╰", "╯", false))
	}

	return out.String()
}

func placeDayEvents(events []CalendarEvent, day time.Time, lph int) []placedEvent {
	dayKey := day.Format("2006-01-02")
	return placeEvents(events, func(ev CalendarEvent) int {
		if ev.Day.Format("2006-01-02") == dayKey {
			return 0
		}
		return -1
	}, lph)
}

func dayAllDayCount(events []CalendarEvent, day time.Time) int {
	dayKey := day.Format("2006-01-02")
	count := 0
	for _, ev := range events {
		if ev.AllDay && ev.Day.Format("2006-01-02") == dayKey {
			count++
		}
	}
	return count
}

func renderDayAllDayRows(events []CalendarEvent, day time.Time, colWidth int, numRows int) string {
	dayKey := day.Format("2006-01-02")
	var allDayEvents []CalendarEvent
	for _, ev := range events {
		if ev.AllDay && ev.Day.Format("2006-01-02") == dayKey {
			allDayEvents = append(allDayEvents, ev)
		}
	}

	faint := lipgloss.NewStyle().Faint(true)
	faintSep := faint.Render("│")

	var out strings.Builder
	for row := range numRows {
		if row == 0 {
			out.WriteString(faint.Render("All day") + " ")
		} else {
			out.WriteString("        ")
		}
		out.WriteString(faintSep)
		if row < len(allDayEvents) {
			out.WriteString(renderEventPill(allDayEvents[row], colWidth, false))
		} else {
			out.WriteString(strings.Repeat(" ", colWidth))
		}
		out.WriteString("\n")
	}
	return out.String()
}

func renderDayHRule(colWidth int, left, right string, timeCol bool) string {
	faint := lipgloss.NewStyle().Faint(true)
	var b strings.Builder
	if timeCol {
		b.WriteString(faint.Render("────────"))
	} else {
		b.WriteString("        ")
	}
	b.WriteString(faint.Render(left))
	b.WriteString(faint.Render(strings.Repeat("─", colWidth)))
	if right != "" {
		b.WriteString(faint.Render(right))
	}
	return b.String()
}
