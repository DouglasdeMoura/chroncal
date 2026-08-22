package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"
	"unicode/utf8"

	lipgloss "charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

type CalendarOptions struct {
	Month            time.Time
	Events           []CalendarEvent
	Today            time.Time
	Selected         time.Time
	WeekStartsOn     time.Weekday
	Width            int
	Height           int
	ShowHeader       bool
	ShowAdjacentDays bool
	ShowWeekNumbers  bool
	// SelectedColor, when non-nil, redraws the selected cell's borders in
	// this color. Use the theme's text color for a "highlighted cursor" look.
	SelectedColor color.Color
}

// isoWeekForRow returns the ISO week number of the Thursday within the
// week row that starts at anchor+week*7 (with weekStart as the first
// column). Thursday is always in the same ISO week as the row's dates.
func isoWeekForRow(anchor time.Time, weekStart time.Weekday, week int) int {
	thursdayOffset := (int(time.Thursday) - int(weekStart) + 7) % 7
	d := anchor.AddDate(0, 0, week*7+thursdayOffset)
	_, w := d.ISOWeek()
	return w
}

// weekdayOffset returns how many days d falls after weekStart, in 0..6.
func weekdayOffset(d time.Time, weekStart time.Weekday) int {
	return (int(d.Weekday()) - int(weekStart) + 7) % 7
}

// calendarGridAnchor returns the date of the top-left cell of the calendar
// grid for the given month and week start.
func calendarGridAnchor(month time.Time, weekStart time.Weekday) time.Time {
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.Local)
	return first.AddDate(0, 0, -weekdayOffset(first, weekStart))
}

// distributeCells splits avail into n cell sizes each >= minSize. It spreads
// the remainder across cells at the start so the row/column sums to avail.
func distributeCells(avail, n, minSize int) []int {
	base := max(avail/n, minSize)
	rem := max(avail-base*n, 0)
	sizes := make([]int, n)
	for i := range n {
		sizes[i] = base
		if i < rem {
			sizes[i]++
		}
	}
	return sizes
}

// calendarGridSizes returns the per-column widths, per-row heights, and the
// number of preamble lines above the 6-row table for a Width×Height month grid.
func calendarGridSizes(width, height int, showHeader bool) (cellWs, cellHs []int, preambleLines int) {
	// Preamble lines above the table: title + blank (optional) + weekday row.
	preambleLines = 1
	if showHeader {
		preambleLines += 2
	}
	// Table overhead: 8 vertical borders between 7 columns.
	cellWs = distributeCells(width-8, 7, 6)
	// Table overhead: top + bottom + 5 inter-row borders = 7 chrome lines
	// above the 6 week rows (no header row — weekdays render as preamble).
	cellHs = distributeCells(height-preambleLines-7, 6, 2)
	return cellWs, cellHs, preambleLines
}

// Calendar renders a full-size month grid that fills Width×Height.
// Returns "" if Width or Height is zero.
func Calendar(opts CalendarOptions) string {
	if opts.Width <= 0 || opts.Height <= 0 {
		return ""
	}

	anchor := calendarGridAnchor(opts.Month, opts.WeekStartsOn)
	first := time.Date(opts.Month.Year(), opts.Month.Month(), 1, 0, 0, 0, 0, time.Local)

	eventsByDay := make(map[string][]CalendarEvent)
	for _, ev := range opts.Events {
		key := ev.Day.Format("2006-01-02")
		eventsByDay[key] = append(eventsByDay[key], ev)
	}

	todayKey := ""
	if !opts.Today.IsZero() {
		todayKey = opts.Today.Local().Format("2006-01-02")
	}
	selectedKey := ""
	if !opts.Selected.IsZero() {
		selectedKey = opts.Selected.Local().Format("2006-01-02")
	}

	cellWs, cellHs, _ := calendarGridSizes(opts.Width, opts.Height, opts.ShowHeader)

	rows := make([][]string, 6)
	for week := range 6 {
		row := make([]string, 7)
		weekLabel := ""
		if opts.ShowWeekNumbers {
			weekLabel = fmt.Sprintf("W%d", isoWeekForRow(anchor, opts.WeekStartsOn, week))
		}
		for col := range 7 {
			d := anchor.AddDate(0, 0, week*7+col)
			dayKey := d.Format("2006-01-02")
			inMonth := d.Month() == first.Month() && d.Year() == first.Year()

			label := ""
			if col == 0 {
				label = weekLabel
			}

			if !inMonth && !opts.ShowAdjacentDays {
				row[col] = blankCellWithWeekLabel(cellWs[col], cellHs[week], label)
				continue
			}
			row[col] = buildCalendarCell(d, dayKey == todayKey, dayKey == selectedKey, inMonth, eventsByDay[dayKey], cellWs[col], cellHs[week], label)
		}
		rows[week] = row
	}

	t := table.New().
		Rows(rows...).
		Border(lipgloss.RoundedBorder()).
		BorderRow(true).
		BorderStyle(lipgloss.NewStyle().Faint(true)).
		StyleFunc(func(_, col int) lipgloss.Style {
			return lipgloss.NewStyle().Width(cellWs[col]).Padding(0, 0)
		})

	rendered := t.Render()
	if opts.SelectedColor != nil && !opts.Selected.IsZero() {
		sr, sc := findCellIndex(anchor, opts.Selected)
		if sr >= 0 {
			rendered = highlightCellBorder(rendered, sr, sc, cellWs, cellHs, opts.SelectedColor)
		}
	}

	var out strings.Builder
	if opts.ShowHeader {
		out.WriteString(lipgloss.NewStyle().Bold(true).Width(opts.Width).Align(lipgloss.Left).Render(first.Format("January 2006")))
		out.WriteString("\n\n")
	}
	out.WriteString(renderWeekdayRow(anchor, cellWs))
	out.WriteString("\n")
	out.WriteString(rendered)
	return out.String()
}

// renderWeekdayRow returns a single-line row of centered, faint weekday
// labels whose columns align with the calendar table below. The row pads
// with a space at the start, end, and inner where the table's vertical
// borders would sit so widths match exactly.
func renderWeekdayRow(anchor time.Time, cellWs []int) string {
	var b strings.Builder
	b.WriteString(" ")
	for i := range 7 {
		if i > 0 {
			b.WriteString(" ")
		}
		label := strings.ToLower(anchor.AddDate(0, 0, i).Format("Mon"))
		b.WriteString(lipgloss.NewStyle().
			Width(cellWs[i]).
			Align(lipgloss.Center).
			Faint(true).
			Render(label))
	}
	b.WriteString(" ")
	return b.String()
}

func findCellIndex(anchor, d time.Time) (int, int) {
	target := d.Local().Format("2006-01-02")
	for week := range 6 {
		for col := range 7 {
			cell := anchor.AddDate(0, 0, week*7+col)
			if cell.Format("2006-01-02") == target {
				return week, col
			}
		}
	}
	return -1, -1
}

func highlightCellBorder(rendered string, sr, sc int, cellWs, cellHs []int, c color.Color) string {
	leftC := sc
	for i := range sc {
		leftC += cellWs[i]
	}
	rightC := leftC + cellWs[sc] + 1

	topL := 0
	for i := range sr {
		topL += cellHs[i] + 1
	}
	botL := topL + cellHs[sr] + 1

	style := lipgloss.NewStyle().Foreground(c).Bold(true).Faint(false)

	lines := strings.Split(rendered, "\n")
	for y := topL; y <= botL && y >= 0 && y < len(lines); y++ {
		if y == topL {
			lines[y] = substituteAtVisPos(lines[y], map[int]rune{leftC: '╭', rightC: '╮'})
			lines[y] = lipgloss.StyleRanges(lines[y],
				lipgloss.NewRange(leftC, rightC+1, style))
			continue
		}
		if y == botL {
			lines[y] = substituteAtVisPos(lines[y], map[int]rune{leftC: '╰', rightC: '╯'})
			lines[y] = lipgloss.StyleRanges(lines[y],
				lipgloss.NewRange(leftC, rightC+1, style))
			continue
		}
		lines[y] = lipgloss.StyleRanges(lines[y],
			lipgloss.NewRange(leftC, leftC+1, style),
			lipgloss.NewRange(rightC, rightC+1, style))
	}
	return strings.Join(lines, "\n")
}

func substituteAtVisPos(line string, subs map[int]rune) string {
	if len(subs) == 0 {
		return line
	}
	var out strings.Builder
	out.Grow(len(line))
	vis := 0
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) {
				b := line[j]
				if b >= 0x40 && b <= 0x7e {
					j++
					break
				}
				j++
			}
			out.WriteString(line[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if repl, ok := subs[vis]; ok {
			out.WriteRune(repl)
		} else {
			out.WriteRune(r)
		}
		vis++
		i += size
	}
	return out.String()
}

func blankCellWithWeekLabel(w, h int, weekLabel string) string {
	line := strings.Repeat(" ", w)
	lines := make([]string, h)
	for i := range h {
		lines[i] = line
	}
	if weekLabel != "" && h > 0 {
		lines[0] = renderWeekLabelLine(weekLabel, "", w)
	}
	return strings.Join(lines, "\n")
}

// renderWeekLabelLine composes the first line of a calendar cell: the
// faint "Wnn" label on the left, and the day-number block (already styled)
// right-aligned. It is padded so the whole line is exactly cellW wide.
func renderWeekLabelLine(weekLabel, dayRendered string, cellW int) string {
	faintLabel := lipgloss.NewStyle().Faint(true).Render(weekLabel)
	labelW := lipgloss.Width(faintLabel)
	dayW := lipgloss.Width(dayRendered)
	pad := max(cellW-labelW-dayW, 0)
	return faintLabel + strings.Repeat(" ", pad) + dayRendered
}

func buildCalendarCell(d time.Time, isToday, isSelected, inMonth bool, events []CalendarEvent, cellW, cellH int, weekLabel string) string {
	dayNum := fmt.Sprintf("%d", d.Day())

	numStyle := lipgloss.NewStyle()
	switch {
	case isToday:
		// Explicit fg/bg rather than Reverse(true): some terminal themes
		// (e.g. Omarchy light themes with OSC-11 cream backgrounds) don't
		// swap reliably between the ANSI 0/7 palette and the OSC-10/11
		// defaults, which can collapse Reverse to fg==bg.
		numStyle = numStyle.
			Background(activeTheme.Today).
			Foreground(activeTheme.Surface).
			Bold(true).
			Padding(0, 1)
	case isSelected:
		// Paint the selected day's number in the same accent used by the
		// cell-border highlight, so the cursor day pops even when the
		// border alone is subtle against the terminal bg.
		numStyle = numStyle.Foreground(activeTheme.Primary).Bold(true)
	case !inMonth:
		numStyle = numStyle.Faint(true)
	}

	rendered := numStyle.Render(dayNum)
	var numLine string
	if weekLabel != "" {
		numLine = renderWeekLabelLine(weekLabel, rendered, cellW)
	} else {
		padW := max(cellW-lipgloss.Width(rendered), 0)
		numLine = strings.Repeat(" ", padW) + rendered
	}

	maxEventLines := cellH - 1
	pills := make([]string, 0, maxEventLines)
	overflow := 0
	for i, ev := range events {
		if i >= maxEventLines {
			overflow = len(events) - maxEventLines + 1
			break
		}
		pills = append(pills, renderEventPill(ev, cellW, !inMonth))
	}
	if overflow > 0 && len(pills) > 0 {
		pills[len(pills)-1] = lipgloss.NewStyle().Faint(true).
			Width(cellW).Render(fmt.Sprintf(" +%d more", overflow))
	}

	lines := make([]string, 0, cellH)
	lines = append(lines, numLine)
	lines = append(lines, pills...)
	blank := strings.Repeat(" ", cellW)
	for len(lines) < cellH {
		lines = append(lines, blank)
	}
	return strings.Join(lines, "\n")
}
