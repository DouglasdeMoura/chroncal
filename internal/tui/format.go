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
	Title     string
	Color     string // hex like "#a6e3a1"; empty → default muted background
	AllDay    bool
	Day       time.Time // local day the event should render on
	StartTime time.Time
	EndTime   time.Time
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
			row[col] = buildCalendarCell(d, dayKey == todayKey, inMonth, eventsByDay[dayKey], cellWs[col], cellHs[week])
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

func buildCalendarCell(d time.Time, isToday, inMonth bool, events []CalendarEvent, cellW, cellH int) string {
	dayNum := fmt.Sprintf("%d", d.Day())

	numStyle := lipgloss.NewStyle()
	switch {
	case isToday:
		numStyle = numStyle.Reverse(true).Bold(true).Padding(0, 1)
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

type WeekOptions struct {
	WeekStart     time.Time
	Events        []CalendarEvent
	Today         time.Time
	Selected      time.Time
	Width         int
	Height        int
	ShowHeader    bool
	SelectedColor color.Color
	ScrollOffset  int
	LinesPerHour  int
}

type placedEvent struct {
	event    CalendarEvent
	col      int
	startRow int
	endRow   int
}

func calcWeekColWidths(width int) []int {
	separators := 8
	availW := width - weekTimeLabelWidth - separators
	if availW < 7 {
		availW = 7
	}
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
	var placed []placedEvent
	totalRows := totalHours * lph
	for _, ev := range events {
		if ev.AllDay {
			continue
		}
		col := findWeekCol(anchor, ev.Day)
		if col < 0 {
			continue
		}
		startRow := ev.StartTime.Hour()*lph + ev.StartTime.Minute()*lph/60
		endRow := startRow + 1
		if !ev.EndTime.IsZero() {
			endRow = ev.EndTime.Hour()*lph + ev.EndTime.Minute()*lph/60
		}
		if endRow <= startRow {
			endRow = startRow + 1
		}
		if endRow > totalRows {
			endRow = totalRows
		}
		placed = append(placed, placedEvent{
			event:    ev,
			col:      col,
			startRow: startRow,
			endRow:   endRow,
		})
	}
	return placed
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

	allDayRows := weekAllDayRowCount(opts.Events, anchor)
	if allDayRows < 1 {
		allDayRows = 1
	}

	headerLines := 0
	if opts.ShowHeader {
		headerLines = 2
	}
	fixedLines := headerLines + 1 + 1 + allDayRows + 1
	viewportHeight := opts.Height - fixedLines
	if viewportHeight < 1 {
		viewportHeight = 1
	}

	scrollOffset := opts.ScrollOffset
	maxScroll := totalRows - viewportHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scrollOffset > maxScroll {
		scrollOffset = maxScroll
	}
	if scrollOffset < 0 {
		scrollOffset = 0
	}

	now := time.Now().Local()
	nowRow := now.Hour()*lph + now.Minute()*lph/60
	nowTimeLabel := now.Format("15:04")
	nowCol := findWeekCol(anchor, now)
	nowHasLine := nowCol >= 0

	faint := lipgloss.NewStyle().Faint(true)
	faintSep := faint.Render("│")
	nowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
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
		out.WriteString(lipgloss.NewStyle().Bold(true).Width(opts.Width).Align(lipgloss.Center).Render(title))
		out.WriteString("\n\n")
	}

	out.WriteString(renderWeekColumnHeaders(anchor, colWs, todayKey, selectedKey, opts.SelectedColor))
	out.WriteString("\n")

	out.WriteString(renderWeekHRule(colWs, "┌", "┬", "┐"))
	out.WriteString("\n")

	out.WriteString(renderWeekAllDayRows(opts.Events, anchor, colWs, allDayRows, selectedCol, opts.SelectedColor))

	out.WriteString(renderWeekHRule(colWs, "├", "┼", "┤"))
	out.WriteString("\n")

	for row := scrollOffset; row < scrollOffset+viewportHeight && row < totalRows; row++ {
		if row > scrollOffset {
			out.WriteString("\n")
		}

		hour := row / lph
		subRow := row % lph

		if row == nowRow {
			out.WriteString(nowStyle.Render(nowTimeLabel) + " ")
		} else if subRow == 0 {
			out.WriteString(faint.Render(fmt.Sprintf("%02d:00", hour)) + " ")
		} else {
			out.WriteString("      ")
		}

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
				p, found := findPlacedEvent(placed, row, i)
				if found {
					out.WriteString(renderTimeCellContent(p, row, colWs[i]))
				} else if nowHasLine && row == nowRow && i == nowCol {
					out.WriteString(nowStyle.Render(strings.Repeat("─", colWs[i])))
				} else {
					out.WriteString(strings.Repeat(" ", colWs[i]))
				}
			}
		}
	}

	return out.String()
}

func renderWeekColumnHeaders(anchor time.Time, colWs []int, todayKey, selectedKey string, selectedColor color.Color) string {
	var b strings.Builder
	b.WriteString("      ")
	b.WriteString(" ")
	for i := range 7 {
		if i > 0 {
			b.WriteString(" ")
		}
		d := anchor.AddDate(0, 0, i)
		dayKey := d.Format("2006-01-02")
		label := strings.ToLower(d.Format("Mon")) + " " + fmt.Sprintf("%d", d.Day())
		style := lipgloss.NewStyle().Width(colWs[i]).Align(lipgloss.Center).Faint(true)
		if dayKey == todayKey {
			style = style.Faint(false).Bold(true)
		}
		if dayKey == selectedKey && selectedColor != nil {
			style = style.Foreground(selectedColor).Bold(true).Faint(false)
		}
		b.WriteString(style.Render(label))
	}
	b.WriteString(" ")
	return b.String()
}

func renderWeekHRule(colWs []int, left, mid, right string) string {
	faint := lipgloss.NewStyle().Faint(true)
	var b strings.Builder
	b.WriteString(faint.Render("──────" + left))
	for i, w := range colWs {
		b.WriteString(faint.Render(strings.Repeat("─", w)))
		if i < len(colWs)-1 {
			b.WriteString(faint.Render(mid))
		}
	}
	b.WriteString(faint.Render(right))
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
	for row := 0; row < numRows; row++ {
		out.WriteString("      ")
		for i := 0; i <= 7; i++ {
			highlighted := selSep != "" && (i == selectedCol || i == selectedCol+1)
			if highlighted {
				out.WriteString(selSep)
			} else {
				out.WriteString(faintSep)
			}
			if i < 7 {
				if row < len(eventsByCol[i]) {
					out.WriteString(renderEventPill(eventsByCol[i][row], colWs[i]))
				} else {
					out.WriteString(strings.Repeat(" ", colWs[i]))
				}
			}
		}
		out.WriteString("\n")
	}

	return out.String()
}

func findPlacedEvent(placed []placedEvent, row, col int) (placedEvent, bool) {
	for _, p := range placed {
		if p.col == col && row >= p.startRow && row < p.endRow {
			return p, true
		}
	}
	return placedEvent{}, false
}

func renderTimeCellContent(p placedEvent, row, width int) string {
	relRow := row - p.startRow
	bg := lipgloss.Color("8")
	fg := lipgloss.Color("15")
	if p.event.Color != "" {
		bg = lipgloss.Color(p.event.Color)
		fg = lipgloss.Color("0")
	}

	var text string
	switch relRow {
	case 0:
		text = " " + p.event.Title
	case 1:
		if !p.event.EndTime.IsZero() {
			text = " " + p.event.StartTime.Format("15:04") + "–" + p.event.EndTime.Format("15:04")
		}
	}

	if lipgloss.Width(text) > width {
		r := []rune(text)
		limit := width - 1
		if limit < 1 {
			limit = 1
		}
		if len(r) > limit {
			text = string(r[:limit]) + "…"
		}
	}

	return lipgloss.NewStyle().Background(bg).Foreground(fg).Width(width).Render(text)
}

func findWeekCol(anchor, d time.Time) int {
	target := d.Local().Format("2006-01-02")
	for col := range 7 {
		if anchor.AddDate(0, 0, col).Format("2006-01-02") == target {
			return col
		}
	}
	return -1
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
