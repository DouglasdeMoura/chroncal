package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"
	"unicode/utf8"

	lipgloss "charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// formatTimeColumn returns a fixed-width label for an event's time slot.
// The width matches "15:04-15:04" (11 chars) so titles line up across
// all-day events, events with only a start time, and events with a range.
func formatTimeColumn(ev event.Event) string {
	switch {
	case ev.AllDay:
		return "           "
	case ev.EndTime.IsZero():
		return ev.StartTime.Local().Format("15:04") + "      "
	default:
		return ev.StartTime.Local().Format("15:04") + "–" + ev.EndTime.Local().Format("15:04")
	}
}

type FormatEventListOptions struct {
	Events      []event.Event
	ShowHeader  bool
	ShowAllDays bool
	From        time.Time
	To          time.Time
	// WeekdayWidth controls the weekday label width (1, 2, or 3 chars).
	// Zero or out-of-range values default to 3.
	WeekdayWidth int
	// ShowWeekday controls whether the weekday label is displayed.
	ShowWeekday bool
	// ShowMonth controls whether the month label is displayed in the day prefix.
	ShowMonth bool
}

// formatWeekday returns a 1-, 2-, or 3-character English weekday label.
func formatWeekday(t time.Time, width int) string {
	full := t.Format("Mon")
	switch width {
	case 1, 2:
		return full[:width]
	default:
		return full
	}
}

func FormatEventList(opts FormatEventListOptions) string {
	if len(opts.Events) == 0 && !opts.ShowAllDays {
		return ""
	}

	weekdayWidth := opts.WeekdayWidth
	if weekdayWidth < 1 || weekdayWidth > 3 {
		weekdayWidth = 3
	}

	eventsByDay := make(map[string][]event.Event)
	for _, ev := range opts.Events {
		key := ev.StartTime.Local().Format("2006-01-02")
		eventsByDay[key] = append(eventsByDay[key], ev)
	}

	months := make(map[string][]string)
	var monthOrder []string

	addDay := func(d time.Time) {
		monthKey := d.Format("2006-01")
		dayKey := d.Format("2006-01-02")
		if _, exists := months[monthKey]; !exists {
			monthOrder = append(monthOrder, monthKey)
		}
		months[monthKey] = append(months[monthKey], dayKey)
	}

	if opts.ShowAllDays && !opts.From.IsZero() && !opts.To.IsZero() {
		from := time.Date(opts.From.Year(), opts.From.Month(), opts.From.Day(), 0, 0, 0, 0, time.Local)
		to := time.Date(opts.To.Year(), opts.To.Month(), opts.To.Day(), 0, 0, 0, 0, time.Local)
		for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
			addDay(d)
		}
	} else {
		seen := make(map[string]bool)
		for _, ev := range opts.Events {
			dayKey := ev.StartTime.Local().Format("2006-01-02")
			if !seen[dayKey] {
				seen[dayKey] = true
				addDay(ev.StartTime.Local())
			}
		}
	}

	var out string
	for _, monthKey := range monthOrder {
		if opts.ShowHeader {
			t, _ := time.Parse("2006-01", monthKey)
			out += lipgloss.NewStyle().Bold(true).Render(t.Format("January 2006")) + "\n\n"
		}

		for _, dayKey := range months[monthKey] {
			dayEvents := eventsByDay[dayKey]
			d, _ := time.Parse("2006-01-02", dayKey)
			dayPrefix := d.Format("02")
			if opts.ShowWeekday {
				dayPrefix += " " + formatWeekday(d, weekdayWidth)
			}
			if opts.ShowMonth {
				dayPrefix = d.Format("Jan") + " " + dayPrefix
			}

			if len(dayEvents) == 0 {
				out += dayPrefix + "\n"
				continue
			}

			continuation := strings.Repeat(" ", len(dayPrefix))
			for i, ev := range dayEvents {
				if i == 0 {
					out += dayPrefix
				} else {
					out += continuation
				}
				out += " " + formatTimeColumn(ev) + "  " + ev.Title + "\n"
			}
		}

		if opts.ShowHeader {
			out += "\n"
		}
	}

	return out
}

// CalendarEvent is the rendering-only view of an event inside the month grid.
// Callers resolve colors and other domain data before passing these in.
type CalendarEvent struct {
	Title  string
	Color  string // hex like "#a6e3a1"; empty → default muted background
	AllDay bool
	Day    time.Time // local day the event should render on
}

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
	// SelectedColor, when non-nil, redraws the selected cell's borders in
	// this color. Use the theme's text color for a "highlighted cursor" look.
	SelectedColor color.Color
}

// Calendar renders a full-size month grid that fills Width×Height.
// Returns "" if Width or Height is zero.
func Calendar(opts CalendarOptions) string {
	if opts.Width <= 0 || opts.Height <= 0 {
		return ""
	}

	first := time.Date(opts.Month.Year(), opts.Month.Month(), 1, 0, 0, 0, 0, time.Local)
	offset := (int(first.Weekday()) - int(opts.WeekStartsOn) + 7) % 7
	anchor := first.AddDate(0, 0, -offset)

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

	// Preamble lines above the table: title + blank (optional) + weekday row.
	preambleLines := 1
	if opts.ShowHeader {
		preambleLines += 2
	}

	// Table overhead: 8 vertical borders between 7 columns. Distribute the
	// remainder across the first columns so the grid fills Width exactly.
	cellWs := make([]int, 7)
	availW := opts.Width - 8
	baseW := availW / 7
	if baseW < 6 {
		baseW = 6
	}
	remW := availW - baseW*7
	if remW < 0 {
		remW = 0
	}
	for i := range 7 {
		cellWs[i] = baseW
		if i < remW {
			cellWs[i]++
		}
	}

	// Table overhead: top + bottom + 5 inter-row borders = 7 chrome lines
	// above the 6 week rows (no header row — weekdays render as preamble).
	cellHs := make([]int, 6)
	availH := opts.Height - preambleLines - 7
	baseH := availH / 6
	if baseH < 2 {
		baseH = 2
	}
	remH := availH - baseH*6
	if remH < 0 {
		remH = 0
	}
	for i := range 6 {
		cellHs[i] = baseH
		if i < remH {
			cellHs[i]++
		}
	}

	rows := make([][]string, 6)
	for week := range 6 {
		row := make([]string, 7)
		for col := range 7 {
			d := anchor.AddDate(0, 0, week*7+col)
			dayKey := d.Format("2006-01-02")
			inMonth := d.Month() == first.Month() && d.Year() == first.Year()

			if !inMonth && !opts.ShowAdjacentDays {
				row[col] = blankCell(cellWs[col], cellHs[week])
				continue
			}
			row[col] = buildCalendarCell(d, dayKey == todayKey, dayKey == selectedKey, inMonth, eventsByDay[dayKey], cellWs[col], cellHs[week])
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
		out.WriteString(lipgloss.NewStyle().Bold(true).Width(opts.Width).Align(lipgloss.Center).Render(first.Format("January 2006")))
		out.WriteString("\n\n")
	}
	out.WriteString(renderWeekdayRow(anchor, cellWs))
	out.WriteString("\n")
	out.WriteString(rendered)
	return out.String()
}

// renderWeekdayRow returns a single-line row of centered, faint weekday
// labels whose columns align with the calendar table below. The row pads
// with a leading/trailing/inner space where the table's vertical borders
// would sit so widths match exactly.
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

// findCellIndex returns the (week, col) position of day d in the 6×7 grid
// anchored at the given start date, or (-1, -1) if it is not present.
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

// highlightCellBorder recolors the four border sides around the cell at
// (sr, sc) in the rendered table output. The corner characters are swapped
// for rounded variants so the highlight reads as an isolated rectangle
// instead of bleeding into adjacent cells via ┼'s outward arms.
func highlightCellBorder(rendered string, sr, sc int, cellWs, cellHs []int, c color.Color) string {
	leftC := sc
	for i := 0; i < sc; i++ {
		leftC += cellWs[i]
	}
	rightC := leftC + cellWs[sc] + 1

	topL := 0
	for i := 0; i < sr; i++ {
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

// substituteAtVisPos replaces runes at the given visible (column) positions
// with the supplied replacements, preserving any interleaved ANSI escape
// sequences unchanged.
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

func blankCell(w, h int) string {
	line := strings.Repeat(" ", w)
	lines := make([]string, h)
	for i := range h {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func buildCalendarCell(d time.Time, isToday, isSelected, inMonth bool, events []CalendarEvent, cellW, cellH int) string {
	dayNum := fmt.Sprintf("%d", d.Day())

	numStyle := lipgloss.NewStyle()
	switch {
	case isToday && isSelected:
		numStyle = numStyle.Reverse(true).Bold(true).Underline(true).Padding(0, 1)
	case isToday:
		numStyle = numStyle.Reverse(true).Bold(true).Padding(0, 1)
	case isSelected:
		numStyle = numStyle.Reverse(true).Underline(true).Padding(0, 1)
	case !inMonth:
		numStyle = numStyle.Faint(true)
	}

	rendered := numStyle.Render(dayNum)
	padW := cellW - lipgloss.Width(rendered)
	if padW < 0 {
		padW = 0
	}
	numLine := strings.Repeat(" ", padW) + rendered

	maxEventLines := cellH - 1
	pills := make([]string, 0, maxEventLines)
	overflow := 0
	for i, ev := range events {
		if i >= maxEventLines {
			overflow = len(events) - maxEventLines + 1
			break
		}
		pills = append(pills, renderEventPill(ev, cellW))
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

func renderEventPill(ev CalendarEvent, cellW int) string {
	text := " " + ev.Title
	if lipgloss.Width(text) > cellW {
		r := []rune(text)
		limit := cellW - 1
		if limit < 1 {
			limit = 1
		}
		if len(r) > limit {
			text = string(r[:limit]) + "…"
		}
	}

	bg := lipgloss.Color("8")
	fg := lipgloss.Color("15")
	if ev.Color != "" {
		bg = lipgloss.Color(ev.Color)
		fg = lipgloss.Color("0")
	}
	return lipgloss.NewStyle().Background(bg).Foreground(fg).
		Width(cellW).Render(text)
}
