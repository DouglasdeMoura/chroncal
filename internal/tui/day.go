package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/tcal/internal/event"
)

type dayView struct {
	events   []event.Event
	selected int
}

func newDayView() dayView {
	return dayView{}
}

func (d *dayView) setEvents(events []event.Event) {
	d.events = events
	if d.selected >= len(events) {
		d.selected = max(0, len(events)-1)
	}
}

func (d *dayView) nextEvent() {
	if d.selected < len(d.events)-1 {
		d.selected++
	}
}

func (d *dayView) prevEvent() {
	if d.selected > 0 {
		d.selected--
	}
}

func (d *dayView) selectedEvent() *event.Event {
	if len(d.events) == 0 {
		return nil
	}
	return &d.events[d.selected]
}

func (d dayView) view(dateLabel string) string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(dateLabel))
	b.WriteString("\n\n")

	if len(d.events) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(DefaultTheme.Muted).Render("  No events for this day"))
		b.WriteString("\n")
		return b.String()
	}

	// All-day events first
	var allDay, timed []event.Event
	for _, e := range d.events {
		if e.AllDay {
			allDay = append(allDay, e)
		} else {
			timed = append(timed, e)
		}
	}

	idx := 0
	if len(allDay) > 0 {
		b.WriteString(subtitleStyle.Render("  All Day"))
		b.WriteString("\n")
		for _, e := range allDay {
			prefix := "  "
			if idx == d.selected {
				prefix = "▸ "
			}
			dot := eventDotStyle.Render("●")
			title := eventTitleStyle.Render(e.Title)
			b.WriteString(fmt.Sprintf("%s%s %s\n", prefix, dot, title))
			idx++
		}
		b.WriteString("\n")
	}

	if len(timed) > 0 {
		b.WriteString(subtitleStyle.Render("  Schedule"))
		b.WriteString("\n")
		for _, e := range timed {
			prefix := "  "
			if idx == d.selected {
				prefix = "▸ "
			}
			dot := eventDotStyle.Render("●")
			timeRange := fmt.Sprintf("%s–%s",
				e.StartTime.Local().Format("15:04"),
				e.EndTime.Local().Format("15:04"))
			t := eventTimeStyle.Render(timeRange)
			title := eventTitleStyle.Render(e.Title)
			b.WriteString(fmt.Sprintf("%s%s %s %s\n", prefix, dot, t, title))
			idx++
		}
	}

	return b.String()
}
