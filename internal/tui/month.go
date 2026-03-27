package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/tcal/internal/event"
)

type monthView struct {
	year     int
	month    time.Month
	selected time.Time
	today    time.Time
	events   map[string][]event.Event // key: "2006-01-02"
	width    int
	height   int
}

func newMonthView() monthView {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	return monthView{
		year:     now.Year(),
		month:    now.Month(),
		selected: today,
		today:    today,
		events:   make(map[string][]event.Event),
	}
}

func (m *monthView) setEvents(events []event.Event) {
	m.events = make(map[string][]event.Event)
	for _, e := range events {
		key := e.StartTime.Local().Format("2006-01-02")
		m.events[key] = append(m.events[key], e)
	}
}

func (m *monthView) nextDay() {
	m.selected = m.selected.AddDate(0, 0, 1)
	m.syncMonth()
}

func (m *monthView) prevDay() {
	m.selected = m.selected.AddDate(0, 0, -1)
	m.syncMonth()
}

func (m *monthView) nextWeek() {
	m.selected = m.selected.AddDate(0, 0, 7)
	m.syncMonth()
}

func (m *monthView) prevWeek() {
	m.selected = m.selected.AddDate(0, 0, -7)
	m.syncMonth()
}

func (m *monthView) nextMonth() {
	m.selected = m.selected.AddDate(0, 1, 0)
	m.syncMonth()
}

func (m *monthView) prevMonth() {
	m.selected = m.selected.AddDate(0, -1, 0)
	m.syncMonth()
}

func (m *monthView) goToToday() {
	m.selected = m.today
	m.syncMonth()
}

func (m *monthView) syncMonth() {
	m.year = m.selected.Year()
	m.month = m.selected.Month()
}

func (m *monthView) dateRange() (time.Time, time.Time) {
	first := time.Date(m.year, m.month, 1, 0, 0, 0, 0, time.Local)
	last := first.AddDate(0, 1, 0)

	// Extend to full weeks
	offset := int(first.Weekday()) - 1 // Monday=0
	if offset < 0 {
		offset = 6
	}
	from := first.AddDate(0, 0, -offset)
	to := last.AddDate(0, 0, 7-int(last.Weekday())%7)
	if to.Before(last) || to.Equal(last) {
		to = to.AddDate(0, 0, 7)
	}
	return from, to
}

func (m monthView) view() string {
	var b strings.Builder

	// Month/year header
	header := fmt.Sprintf("◀  %s %d  ▶", m.month.String(), m.year)
	b.WriteString(titleStyle.Render(header))
	b.WriteString("\n\n")

	// Day-of-week headers
	days := []string{"Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"}
	var dayHeaders []string
	for _, d := range days {
		dayHeaders = append(dayHeaders, dayHeaderStyle.Render(d))
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, dayHeaders...))
	b.WriteString("\n")

	// Calendar grid
	first := time.Date(m.year, m.month, 1, 0, 0, 0, 0, time.Local)
	offset := int(first.Weekday()) - 1
	if offset < 0 {
		offset = 6
	}
	start := first.AddDate(0, 0, -offset)

	for week := 0; week < 6; week++ {
		var cells []string
		allOutside := true
		for day := 0; day < 7; day++ {
			current := start.AddDate(0, 0, week*7+day)
			dayStr := fmt.Sprintf("%d", current.Day())

			// Add event dot indicator
			key := current.Format("2006-01-02")
			if evts, ok := m.events[key]; ok && len(evts) > 0 {
				dayStr += "·"
			} else {
				dayStr += " "
			}

			inMonth := current.Month() == m.month
			isToday := current.Equal(m.today)
			isSelected := current.Equal(m.selected)

			if inMonth {
				allOutside = false
			}

			var style lipgloss.Style
			switch {
			case isSelected:
				style = selectedCellStyle
			case isToday:
				style = todayCellStyle
			case !inMonth:
				style = outsideMonthStyle
			default:
				style = dayCellStyle
			}

			cells = append(cells, style.Render(dayStr))
		}
		if allOutside {
			break
		}
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, cells...))
		b.WriteString("\n")
	}

	// Events for selected day
	b.WriteString("\n")
	selectedKey := m.selected.Format("2006-01-02")
	dateLabel := m.selected.Format("Monday, January 2")
	if m.selected.Equal(m.today) {
		dateLabel = "Today — " + dateLabel
	}
	b.WriteString(subtitleStyle.Render(dateLabel))
	b.WriteString("\n")

	if evts, ok := m.events[selectedKey]; ok && len(evts) > 0 {
		for _, e := range evts {
			var timeStr string
			if e.AllDay {
				timeStr = "all day"
			} else {
				timeStr = e.StartTime.Local().Format("15:04")
			}
			dot := eventDotStyle.Render("●")
			t := eventTimeStyle.Render(timeStr)
			title := eventTitleStyle.Render(e.Title)
			b.WriteString(fmt.Sprintf("  %s %s %s\n", dot, t, title))
		}
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(DefaultTheme.Muted).Render("  No events"))
		b.WriteString("\n")
	}

	return b.String()
}

func (m monthView) selectedEvents() []event.Event {
	key := m.selected.Format("2006-01-02")
	return m.events[key]
}
