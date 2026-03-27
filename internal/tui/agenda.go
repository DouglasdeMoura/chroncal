package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/tcal/internal/event"
)

type agendaView struct {
	events   []event.Event
	selected int
	today    time.Time
}

func newAgendaView() agendaView {
	now := time.Now()
	return agendaView{
		today: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local),
	}
}

func (a *agendaView) setEvents(events []event.Event) {
	a.events = events
	if a.selected >= len(events) {
		a.selected = max(0, len(events)-1)
	}
}

func (a *agendaView) next() {
	if a.selected < len(a.events)-1 {
		a.selected++
	}
}

func (a *agendaView) prev() {
	if a.selected > 0 {
		a.selected--
	}
}

func (a *agendaView) selectedEvent() *event.Event {
	if len(a.events) == 0 {
		return nil
	}
	return &a.events[a.selected]
}

func (a agendaView) view() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Upcoming Events"))
	b.WriteString("\n\n")

	if len(a.events) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(DefaultTheme.Muted).Render("  No upcoming events"))
		b.WriteString("\n")
		return b.String()
	}

	var currentDate string
	for i, e := range a.events {
		localDate := e.StartTime.Local()
		dateKey := localDate.Format("2006-01-02")

		if dateKey != currentDate {
			if currentDate != "" {
				b.WriteString("\n")
			}

			label := a.dateLabel(localDate)
			dateStyle := lipgloss.NewStyle().Bold(true).Foreground(DefaultTheme.Text)
			b.WriteString("  " + dateStyle.Render(label))
			b.WriteString("\n")
			b.WriteString("  " + lipgloss.NewStyle().Foreground(DefaultTheme.Border).Render(strings.Repeat("─", len(label)+2)))
			b.WriteString("\n")
			currentDate = dateKey
		}

		prefix := "  "
		if i == a.selected {
			prefix = "▸ "
		}

		dot := eventDotStyle.Render("●")
		var timeStr string
		if e.AllDay {
			timeStr = eventTimeStyle.Render("all day")
		} else {
			timeStr = eventTimeStyle.Render(localDate.Format("15:04"))
		}
		title := eventTitleStyle.Render(e.Title)

		b.WriteString(fmt.Sprintf("%s%s %s %s\n", prefix, dot, timeStr, title))
	}

	return b.String()
}

func (a agendaView) dateLabel(date time.Time) string {
	d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
	diff := d.Sub(a.today).Hours() / 24

	switch {
	case diff == 0:
		return fmt.Sprintf("Today — %s", date.Format("Monday, January 2"))
	case diff == 1:
		return fmt.Sprintf("Tomorrow — %s", date.Format("Monday, January 2"))
	case diff == -1:
		return fmt.Sprintf("Yesterday — %s", date.Format("Monday, January 2"))
	default:
		return date.Format("Monday, January 2, 2006")
	}
}
