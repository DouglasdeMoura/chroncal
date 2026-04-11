package tui

import (
	"fmt"
	"strings"
	"time"

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
	WeekStartsOn     time.Weekday
	Width            int
	Height           int
	ShowHeader       bool
	ShowAdjacentDays bool
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

	headers := make([]string, 7)
	for i := range 7 {
		headers[i] = strings.ToLower(anchor.AddDate(0, 0, i).Format("Mon"))
	}

	eventsByDay := make(map[string][]CalendarEvent)
	for _, ev := range opts.Events {
		key := ev.Day.Format("2006-01-02")
		eventsByDay[key] = append(eventsByDay[key], ev)
	}

	todayKey := ""
	if !opts.Today.IsZero() {
		todayKey = opts.Today.Local().Format("2006-01-02")
	}

	titleLines := 0
	if opts.ShowHeader {
		titleLines = 2
	}

	// Table overhead: 8 vertical borders between 7 columns.
	cellW := (opts.Width - 8) / 7
	if cellW < 6 {
		cellW = 6
	}
	// Table overhead: top, bottom, header row, header-bottom border,
	// and 5 inter-row borders = 9 chrome lines above 6*cellH.
	cellH := (opts.Height - titleLines - 9) / 6
	if cellH < 2 {
		cellH = 2
	}

	rows := make([][]string, 6)
	for week := range 6 {
		row := make([]string, 7)
		for col := range 7 {
			d := anchor.AddDate(0, 0, week*7+col)
			dayKey := d.Format("2006-01-02")
			inMonth := d.Month() == first.Month() && d.Year() == first.Year()

			if !inMonth && !opts.ShowAdjacentDays {
				row[col] = blankCell(cellW, cellH)
				continue
			}
			row[col] = buildCalendarCell(d, dayKey == todayKey, inMonth, eventsByDay[dayKey], cellW, cellH)
		}
		rows[week] = row
	}

	t := table.New().
		Headers(headers...).
		Rows(rows...).
		BorderRow(true).
		BorderStyle(lipgloss.NewStyle().Faint(true)).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return lipgloss.NewStyle().Width(cellW).Align(lipgloss.Center).Faint(true).Padding(0, 0)
			}
			return lipgloss.NewStyle().Width(cellW).Padding(0, 0)
		})

	var out strings.Builder
	if opts.ShowHeader {
		out.WriteString(lipgloss.NewStyle().Bold(true).Render(first.Format("January 2006")))
		out.WriteString("\n")
	}
	out.WriteString(t.Render())
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
