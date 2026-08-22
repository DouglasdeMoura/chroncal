package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
)

type WeekOptions struct {
	WeekStart       time.Time
	Events          []CalendarEvent
	Today           time.Time
	Selected        time.Time
	Width           int
	Height          int
	ShowHeader      bool
	ShowWeekNumbers bool
	SelectedColor   color.Color
	ScrollOffset    int
	LinesPerHour    int
}

func calcWeekColWidths(width int) []int {
	separators := 8
	availW := max(width-timeLabelWidth-separators, 7)
	colW := availW / 7
	remW := availW - colW*7
	colWs := make([]int, 7)
	for i := range 7 {
		colWs[i] = colW
		if i < remW {
			colWs[i]++
		}
	}
	return colWs
}

func placeWeekEvents(events []CalendarEvent, anchor time.Time, lph int) []placedEvent {
	return placeEvents(events, func(ev CalendarEvent) int {
		return findWeekCol(anchor, ev.Day)
	}, lph)
}

func WeekGrid(opts WeekOptions) string {
	if opts.Width <= 0 || opts.Height <= 0 {
		return ""
	}

	lph := opts.LinesPerHour
	if lph < 1 {
		lph = defaultLinesPerHour
	}
	totalRows := totalHours * lph
	anchor := opts.WeekStart

	todayKey := ""
	if !opts.Today.IsZero() {
		todayKey = opts.Today.Local().Format("2006-01-02")
	}
	selectedKey := ""
	selectedCol := -1
	if !opts.Selected.IsZero() {
		selectedKey = opts.Selected.Local().Format("2006-01-02")
		selectedCol = findWeekCol(anchor, opts.Selected)
	}

	colWs := calcWeekColWidths(opts.Width)
	placed := placeWeekEvents(opts.Events, anchor, lph)
	resolveOverlaps(placed)

	allDayRows := max(weekAllDayRowCount(opts.Events, anchor), 1)

	headerLines := 0
	if opts.ShowHeader {
		headerLines = 2
	}
	fixedLines := headerLines + 1 + 1 + allDayRows + 1
	viewportHeight := max(opts.Height-fixedLines, 1)

	maxScroll := max(totalRows-viewportHeight, 0)
	scrollOffset := min(max(opts.ScrollOffset, 0), maxScroll)

	// Reserve one line for the bottom rule when end of day is visible.
	showBottomRule := scrollOffset+viewportHeight >= totalRows
	if showBottomRule && viewportHeight > 1 {
		viewportHeight--
	}

	now := time.Now().Local()
	nowRow := now.Hour()*lph + now.Minute()*lph/60
	nowTimeLabel := now.Format("15:04")
	nowCol := findWeekCol(anchor, now)
	nowHasLine := nowCol >= 0

	faint := lipgloss.NewStyle().Faint(true)
	faintSep := faint.Render("│")
	nowStyle := lipgloss.NewStyle().Foreground(ActiveTheme().Today).Bold(true)
	nowSep := nowStyle.Render("│")

	var selSep string
	if opts.SelectedColor != nil && selectedCol >= 0 {
		selStyle := lipgloss.NewStyle().Foreground(opts.SelectedColor).Bold(true).Faint(false)
		selSep = selStyle.Render("│")
	}

	var out strings.Builder

	if opts.ShowHeader {
		endDay := anchor.AddDate(0, 0, 6)
		var title string
		if anchor.Month() == endDay.Month() {
			title = fmt.Sprintf("%s %d – %d, %d",
				anchor.Format("January"), anchor.Day(), endDay.Day(), anchor.Year())
		} else if anchor.Year() == endDay.Year() {
			title = fmt.Sprintf("%s %d – %s %d, %d",
				anchor.Format("Jan"), anchor.Day(), endDay.Format("Jan"), endDay.Day(), anchor.Year())
		} else {
			title = fmt.Sprintf("%s %d, %d – %s %d, %d",
				anchor.Format("Jan"), anchor.Day(), anchor.Year(), endDay.Format("Jan"), endDay.Day(), endDay.Year())
		}
		out.WriteString(lipgloss.NewStyle().Bold(true).Width(opts.Width).Align(lipgloss.Left).Render(title))
		out.WriteString("\n\n")
	}

	weekNumLabel := ""
	if opts.ShowWeekNumbers {
		thursdayOffset := (int(time.Thursday) - int(anchor.Weekday()) + 7) % 7
		_, wn := anchor.AddDate(0, 0, thursdayOffset).ISOWeek()
		weekNumLabel = fmt.Sprintf("W%d", wn)
	}
	out.WriteString(renderWeekColumnHeaders(anchor, colWs, todayKey, selectedKey, opts.SelectedColor, weekNumLabel))
	out.WriteString("\n")

	out.WriteString(renderWeekHRule(colWs, "┌", "┬", "", true, selectedCol, opts.SelectedColor))
	out.WriteString("\n")

	out.WriteString(renderWeekAllDayRows(opts.Events, anchor, colWs, allDayRows, selectedCol, opts.SelectedColor))

	out.WriteString(renderWeekHRule(colWs, "├", "┼", "╮", true, selectedCol, opts.SelectedColor))
	out.WriteString("\n")

	for row := scrollOffset; row < scrollOffset+viewportHeight && row < totalRows; row++ {
		if row > scrollOffset {
			out.WriteString("\n")
		}

		out.WriteString(renderTimeLabel(row, lph, row == nowRow, nowTimeLabel))

		for i := 0; i <= 7; i++ {
			nowBorder := nowHasLine && row == nowRow && i == nowCol
			highlighted := selSep != "" && (i == selectedCol || i == selectedCol+1)
			if nowBorder {
				out.WriteString(nowSep)
			} else if highlighted {
				out.WriteString(selSep)
			} else {
				out.WriteString(faintSep)
			}

			if i < 7 {
				matches := findPlacedEvents(placed, row, i)
				if len(matches) > 0 {
					out.WriteString(renderOverlappingCells(matches, row, colWs[i]))
				} else if nowHasLine && row == nowRow && i == nowCol {
					out.WriteString(nowStyle.Render(strings.Repeat("─", colWs[i])))
				} else {
					out.WriteString(strings.Repeat(" ", colWs[i]))
				}
			}
		}
	}

	if showBottomRule {
		out.WriteString("\n")
		out.WriteString(renderWeekHRule(colWs, "╰", "┴", "╯", false, selectedCol, opts.SelectedColor))
	}

	return out.String()
}

func renderWeekColumnHeaders(anchor time.Time, colWs []int, todayKey, selectedKey string, selectedColor color.Color, weekNumLabel string) string {
	var b strings.Builder
	if weekNumLabel != "" {
		b.WriteString(lipgloss.NewStyle().Faint(true).Width(timeLabelWidth).Align(lipgloss.Center).Render(weekNumLabel))
	} else {
		b.WriteString(strings.Repeat(" ", timeLabelWidth))
	}
	b.WriteString(" ")
	for i := range 7 {
		if i > 0 {
			b.WriteString(" ")
		}
		d := anchor.AddDate(0, 0, i)
		dayKey := d.Format("2006-01-02")
		dayName := strings.ToLower(d.Format("Mon"))
		dayNum := fmt.Sprintf("%d", d.Day())
		style := lipgloss.NewStyle().Faint(true)
		numStyle := lipgloss.NewStyle().Faint(true)
		if dayKey == todayKey {
			// Today number is always the filled pill — the cursor never
			// overrides today's identity on the number itself.
			style = style.Faint(false).Bold(true)
			numStyle = numStyle.
				Faint(false).
				Bold(true).
				Background(activeTheme.Today).
				Foreground(activeTheme.Surface).
				Padding(0, 1)
		}
		if dayKey == selectedKey && selectedColor != nil {
			// The weekday label picks up the cursor accent in both cases,
			// but we skip the number when it's also today so the filled
			// pill keeps its today colors.
			style = style.Foreground(selectedColor).Bold(true).Faint(false)
			if dayKey != todayKey {
				numStyle = numStyle.Foreground(selectedColor).Bold(true).Faint(false)
			}
		}
		label := style.Render(dayName) + " " + numStyle.Render(dayNum)
		colStyle := lipgloss.NewStyle().Width(colWs[i]).Align(lipgloss.Center)
		b.WriteString(colStyle.Render(label))
	}
	b.WriteString(" ")
	return b.String()
}

func renderWeekHRule(colWs []int, left, mid, right string, timeCol bool, selectedCol int, selectedColor color.Color) string {
	faint := lipgloss.NewStyle().Faint(true)
	var selStyle lipgloss.Style
	hasSel := selectedColor != nil && selectedCol >= 0
	if hasSel {
		selStyle = lipgloss.NewStyle().Foreground(selectedColor).Bold(true).Faint(false)
	}

	renderJunction := func(s string, sepIdx int) string {
		if hasSel && (sepIdx == selectedCol || sepIdx == selectedCol+1) {
			return selStyle.Render(s)
		}
		return faint.Render(s)
	}

	var b strings.Builder
	if timeCol {
		b.WriteString(faint.Render("────────"))
		b.WriteString(renderJunction(left, 0))
	} else {
		b.WriteString("        ")
		b.WriteString(renderJunction(left, 0))
	}
	for i, w := range colWs {
		seg := strings.Repeat("─", w)
		if hasSel && i == selectedCol {
			b.WriteString(selStyle.Render(seg))
		} else {
			b.WriteString(faint.Render(seg))
		}
		if i < len(colWs)-1 {
			b.WriteString(renderJunction(mid, i+1))
		}
	}
	b.WriteString(renderJunction(right, len(colWs)))
	return b.String()
}

func weekAllDayRowCount(events []CalendarEvent, anchor time.Time) int {
	maxPerCol := 0
	for col := range 7 {
		d := anchor.AddDate(0, 0, col)
		dayKey := d.Format("2006-01-02")
		count := 0
		for _, ev := range events {
			if ev.AllDay && ev.Day.Format("2006-01-02") == dayKey {
				count++
			}
		}
		if count > maxPerCol {
			maxPerCol = count
		}
	}
	return maxPerCol
}

func renderWeekAllDayRows(events []CalendarEvent, anchor time.Time, colWs []int, numRows int, selectedCol int, selectedColor color.Color) string {
	eventsByCol := make([][]CalendarEvent, 7)
	for _, ev := range events {
		if !ev.AllDay {
			continue
		}
		col := findWeekCol(anchor, ev.Day)
		if col >= 0 {
			eventsByCol[col] = append(eventsByCol[col], ev)
		}
	}

	faint := lipgloss.NewStyle().Faint(true)
	faintSep := faint.Render("│")
	var selSep string
	if selectedColor != nil && selectedCol >= 0 {
		selStyle := lipgloss.NewStyle().Foreground(selectedColor).Bold(true).Faint(false)
		selSep = selStyle.Render("│")
	}

	var out strings.Builder
	for row := range numRows {
		if row == 0 {
			out.WriteString(faint.Render("All day") + " ")
		} else {
			out.WriteString("        ")
		}
		for i := range 7 {
			highlighted := selSep != "" && (i == selectedCol || i == selectedCol+1)
			if highlighted {
				out.WriteString(selSep)
			} else {
				out.WriteString(faintSep)
			}
			if row < len(eventsByCol[i]) {
				out.WriteString(renderEventPill(eventsByCol[i][row], colWs[i], false))
			} else {
				out.WriteString(strings.Repeat(" ", colWs[i]))
			}
		}
		out.WriteString("\n")
	}

	return out.String()
}

func findWeekCol(anchor, d time.Time) int {
	// d is already in the correct display timezone (local for timed events,
	// UTC for all-day events via eventDay), so don't call .Local() again.
	target := d.Format("2006-01-02")
	for col := range 7 {
		if anchor.AddDate(0, 0, col).Format("2006-01-02") == target {
			return col
		}
	}
	return -1
}
