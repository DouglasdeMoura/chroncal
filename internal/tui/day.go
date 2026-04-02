package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

type dayView struct {
	events   []event.Event
	todos    []todo.Todo
	selected int // index across both events then todos
}

func newDayView() dayView {
	return dayView{}
}

func (d *dayView) setEvents(events []event.Event) {
	d.events = events
	d.clampSelection()
}

func (d *dayView) setTodos(todos []todo.Todo) {
	d.todos = todos
	d.clampSelection()
}

func (d *dayView) totalItems() int {
	return len(d.events) + len(d.todos)
}

func (d *dayView) clampSelection() {
	total := d.totalItems()
	if d.selected >= total {
		d.selected = max(0, total-1)
	}
}

func (d *dayView) next() {
	if d.selected < d.totalItems()-1 {
		d.selected++
	}
}

func (d *dayView) prev() {
	if d.selected > 0 {
		d.selected--
	}
}

// selectedItem returns the selected event or todo (one will be nil).
func (d *dayView) selectedItem() (*event.Event, *todo.Todo) {
	if d.totalItems() == 0 {
		return nil, nil
	}
	if d.selected < len(d.events) {
		return &d.events[d.selected], nil
	}
	todoIdx := d.selected - len(d.events)
	if todoIdx < len(d.todos) {
		return nil, &d.todos[todoIdx]
	}
	return nil, nil
}

func (d dayView) view(dateLabel string) string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(dateLabel))
	b.WriteString("\n\n")

	if d.totalItems() == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(DefaultTheme.Muted).Render("  No events or todos for this day"))
		b.WriteString("\n")
		return b.String()
	}

	// All-day events
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
			fmt.Fprintf(&b, "%s%s %s\n", prefix, dot, title)
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
			fmt.Fprintf(&b, "%s%s %s %s\n", prefix, dot, t, title)
			idx++
		}
	}

	// Todos
	if len(d.todos) > 0 {
		b.WriteString("\n")
		b.WriteString(subtitleStyle.Render("  Todos"))
		b.WriteString("\n")
		for _, t := range d.todos {
			prefix := "  "
			if idx == d.selected {
				prefix = "▸ "
			}
			check := "○"
			checkColor := DefaultTheme.Accent
			if t.IsCompleted() {
				check = "●"
				checkColor = DefaultTheme.Muted
			} else if t.IsOverdue() {
				checkColor = DefaultTheme.Error
			}
			checkStyle := lipgloss.NewStyle().Foreground(checkColor)
			title := eventTitleStyle.Render(t.Summary)
			fmt.Fprintf(&b, "%s%s %s\n", prefix, checkStyle.Render(check), title)
			idx++
		}
	}

	return b.String()
}
