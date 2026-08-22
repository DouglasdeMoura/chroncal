package tui

import (
	"fmt"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func (m AgendaModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	headerLines := 2
	viewportH := max(m.height-headerLines, 1)

	var out strings.Builder

	if len(m.rows) == 0 {
		headerDay := m.cursor
		out.WriteString(m.renderMonthHeader(headerDay))
		out.WriteString("\n\n")
		out.WriteString(lipgloss.NewStyle().Foreground(m.theme.TextDim).Render("No events this month."))
		out.WriteString("\n\n")
		out.WriteString(DefaultButtonStyles().Normal.Normal.Render("+ Create event"))
		return out.String()
	}

	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	end := min(start+viewportH, len(m.rows))

	// Sticky title uses position:sticky semantics. It reflects the most
	// recent monthHeader at or above the viewport top. When none has
	// scrolled past yet, use the day of the first visible row. Do not
	// use windowStart. Events can start in a later month than windowStart
	// if earlier months are empty (common after a backward expansion).
	// A fallback to windowStart would advertise a month the user cannot
	// see any events from.
	headerDay := m.windowStart
	foundAbove := false
	for i := min(start, len(m.rows)-1); i >= 0; i-- {
		if m.rows[i].monthHeader {
			headerDay = m.rows[i].day
			foundAbove = true
			break
		}
	}
	if !foundAbove && start < len(m.rows) {
		headerDay = m.rows[start].day
	}
	stickyMonth := monthKey(headerDay)
	out.WriteString(m.renderMonthHeader(headerDay))
	out.WriteString("\n\n")

	// Skip any leading separator/monthHeader rows that label the sticky's
	// month. Otherwise the user sees the month name twice back-to-back
	// (once in the sticky, once inline). Extend the render range so the
	// viewport stays filled.
	renderStart := start
	for renderStart < end {
		r := m.rows[renderStart]
		if (!r.monthHeader && !r.separator) || monthKey(r.day) != stickyMonth {
			break
		}
		renderStart++
		end = min(end+1, len(m.rows))
	}

	for i := renderStart; i < end; i++ {
		if i > renderStart {
			out.WriteByte('\n')
		}
		if m.rows[i].separator {
			out.WriteString(lipgloss.NewStyle().Width(m.width).Render(""))
			continue
		}
		if m.rows[i].monthHeader {
			out.WriteString(m.renderMonthHeader(m.rows[i].day))
			continue
		}
		if m.rows[i].emptyDay {
			out.WriteString(m.renderEmptyDayRow(m.rows[i], i == m.selected))
			continue
		}
		out.WriteString(m.renderEventRow(m.rows[i], i == m.selected))
	}
	return out.String()
}

// renderMonthHeader formats an in-list month title using the same style
// as the top-of-view header so month transitions read clearly.
func (m AgendaModel) renderMonthHeader(d time.Time) string {
	style := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Text)
	return lipgloss.NewStyle().Width(m.width).Render(style.Render(d.Format("January 2006")))
}

// renderEmptyDayRow draws a placeholder for a day with no events. When
// selected, surfaces a "+ Create event" affordance to invite the user
// to create on that day.
func (m AgendaModel) renderEmptyDayRow(r agendaRow, selected bool) string {
	// Day column stays unpainted; the selection highlight starts where the
	// time column would begin and runs to the end of the line.
	unpainted := lipgloss.NewStyle()
	highlight := lipgloss.NewStyle()
	if selected {
		highlight = highlight.Background(m.selectedColor)
	}
	dayCol := m.renderDayColumn(r, unpainted, false)
	prefix := unpainted.Render(strings.Repeat(" ", agendaLeftPad)) +
		dayCol +
		unpainted.Render(" "+strings.Repeat(" ", agendaDotColWidth))

	tail := ""
	if selected {
		tail = highlight.Foreground(m.theme.Primary).Bold(true).Render("+ Create event")
	}
	tailW := max(m.width-lipgloss.Width(prefix), 0)
	tailFg := m.theme.Text
	if selected {
		tailFg = m.theme.SelectedText
	}
	return prefix + highlight.Width(tailW).Foreground(tailFg).Render(tail)
}

// renderEventRow composes a single agenda line. When selected, the
// highlight starts at the time column and paints to the end of the line.
// The day column is intentionally left unpainted so the date badge
// remains visually anchored.
func (m AgendaModel) renderEventRow(r agendaRow, selected bool) string {
	ev := r.event

	unpainted := lipgloss.NewStyle()
	highlight := lipgloss.NewStyle()
	if selected {
		highlight = highlight.Background(m.selectedColor).Bold(true)
	}

	dayCol := m.renderDayColumn(r, unpainted, false)

	cal := m.calendars[ev.CalendarID]
	dotColor := m.theme.Muted
	if cal.Color != "" {
		dotColor = lipgloss.Color(cal.Color)
	}
	dot := unpainted.Foreground(dotColor).Render(Glyphs["dot"])

	timeText := agendaTimeText(ev, r.dayIndex, r.totalDays)
	timeFg := m.theme.TextDim
	if selected {
		timeFg = m.theme.SelectedText
	}
	timeStyle := highlight.Foreground(timeFg).Width(agendaTimeColWidth)
	if ev.AllDay {
		timeStyle = timeStyle.Italic(true)
	}
	timeCol := timeStyle.Render(timeText)

	title := ev.Title
	if r.totalDays > 1 {
		title += fmt.Sprintf(" (day %d/%d)", r.dayIndex, r.totalDays)
	}

	prefix := unpainted.Render(strings.Repeat(" ", agendaLeftPad)) +
		dayCol +
		unpainted.Render(" ") +
		unpainted.Width(agendaDotColWidth).Render(" "+dot+" ")

	titleW := max(m.width-lipgloss.Width(prefix)-agendaTimeColWidth, 1)
	titleFg := m.theme.Text
	if selected {
		titleFg = m.theme.SelectedText
	}
	titleCol := highlight.
		Foreground(titleFg).
		Width(titleW).
		Render(truncateTo(title, titleW))

	return prefix + timeCol + titleCol
}

// renderDayColumn returns the 8-column-wide day label shown at the start
// of the first event row of a calendar day. Continuation rows get a blank
// column. Today's day number is rendered in a filled pill using the theme
// "today" color. The day column is never painted by the selection
// highlight. Callers pass an empty base so the date badge stays visually
// anchored regardless of row state.
func (m AgendaModel) renderDayColumn(r agendaRow, base lipgloss.Style, _ bool) string {
	if !r.firstOfDay {
		return base.Render(strings.Repeat(" ", agendaDayColWidth))
	}
	d := r.day
	weekday := d.Format("Mon")
	dayNum := fmt.Sprintf("%d", d.Day())

	isToday := sameDay(d, m.today)

	var weekdayStyle, numStyle lipgloss.Style
	switch {
	case isToday:
		weekdayStyle = base.Foreground(m.theme.Today).Bold(true)
		numStyle = lipgloss.NewStyle().
			Background(m.theme.Today).
			Foreground(m.theme.Surface).
			Bold(true).
			PaddingRight(1)
	default:
		weekdayStyle = base.Foreground(m.theme.TextDim)
		numStyle = base.Foreground(m.theme.Text).Bold(true)
	}

	body := numStyle.Render(dayNum) + base.Render(" ") + weekdayStyle.Render(weekday)
	return base.Width(agendaDayColWidth).Render(body)
}

// agendaTimeText produces the compact time-column text for an event on a
// given day of its span.
func agendaTimeText(ev event.Event, dayIndex, totalDays int) string {
	if ev.AllDay {
		return "All day"
	}
	if totalDays <= 1 {
		start := ev.StartTime.Local().Format("15:04")
		if ev.EndTime.IsZero() {
			return start
		}
		return start + "–" + ev.EndTime.Local().Format("15:04")
	}
	switch dayIndex {
	case 1:
		return ev.StartTime.Local().Format("15:04") + " " + Glyphs["time.arrow"]
	case totalDays:
		return Glyphs["time.arrow"] + " " + ev.EndTime.Local().Format("15:04")
	default:
		return "cont. " + Glyphs["time.arrow"]
	}
}
