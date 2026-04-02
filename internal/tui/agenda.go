package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// agendaItem is a union of event or todo for unified rendering.
type agendaItem struct {
	event *event.Event
	todo  *todo.Todo
	date  time.Time // sort key
}

type agendaView struct {
	items    []agendaItem
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
	a.rebuildItems(events, a.extractTodos())
}

func (a *agendaView) setTodos(todos []todo.Todo) {
	a.rebuildItems(a.extractEvents(), todos)
}

func (a *agendaView) extractEvents() []event.Event {
	var events []event.Event
	for _, item := range a.items {
		if item.event != nil {
			events = append(events, *item.event)
		}
	}
	return events
}

func (a *agendaView) extractTodos() []todo.Todo {
	var todos []todo.Todo
	for _, item := range a.items {
		if item.todo != nil {
			todos = append(todos, *item.todo)
		}
	}
	return todos
}

func (a *agendaView) rebuildItems(events []event.Event, todos []todo.Todo) {
	a.items = nil

	for i := range events {
		a.items = append(a.items, agendaItem{
			event: &events[i],
			date:  events[i].StartTime.Local(),
		})
	}
	for i := range todos {
		if todos[i].DueDate == "" {
			// Todos without due date go at the end
			a.items = append(a.items, agendaItem{
				todo: &todos[i],
				date: time.Date(9999, 1, 1, 0, 0, 0, 0, time.Local),
			})
		} else {
			a.items = append(a.items, agendaItem{
				todo: &todos[i],
				date: todos[i].ParseDueDate().Local(),
			})
		}
	}

	sort.Slice(a.items, func(i, j int) bool {
		return a.items[i].date.Before(a.items[j].date)
	})

	a.clampSelection()
}

func (a *agendaView) clampSelection() {
	if a.selected >= len(a.items) {
		a.selected = max(0, len(a.items)-1)
	}
}

func (a *agendaView) next() {
	if a.selected < len(a.items)-1 {
		a.selected++
	}
}

func (a *agendaView) prev() {
	if a.selected > 0 {
		a.selected--
	}
}

func (a *agendaView) selectedItem() (*event.Event, *todo.Todo) {
	if len(a.items) == 0 {
		return nil, nil
	}
	item := a.items[a.selected]
	return item.event, item.todo
}

func (a agendaView) view() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Upcoming"))
	b.WriteString("\n\n")

	if len(a.items) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(DefaultTheme.Muted).Render("  No upcoming events or todos"))
		b.WriteString("\n")
		return b.String()
	}

	var currentDate string
	for i, item := range a.items {
		dateKey := item.date.Format("2006-01-02")
		if dateKey == "9999-01-01" {
			dateKey = "no-due-date"
		}

		if dateKey != currentDate {
			if currentDate != "" {
				b.WriteString("\n")
			}

			var label string
			if dateKey == "no-due-date" {
				label = "No Due Date"
			} else {
				label = a.dateLabel(item.date)
			}
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

		if item.event != nil {
			e := item.event
			dot := eventDotStyle.Render("●")
			var timeStr string
			if e.AllDay {
				timeStr = eventTimeStyle.Render("all day")
			} else {
				timeStr = eventTimeStyle.Render(e.StartTime.Local().Format("15:04"))
			}
			title := eventTitleStyle.Render(e.Title)
			fmt.Fprintf(&b, "%s%s %s %s\n", prefix, dot, timeStr, title)
		} else if item.todo != nil {
			t := item.todo
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
			fmt.Fprintf(&b, "%s%s %s %s\n", prefix, checkStyle.Render(check), eventTimeStyle.Render("todo"), title)
		}
	}

	return b.String()
}

func (a agendaView) dateLabel(date time.Time) string {
	d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
	diff := d.Sub(a.today).Hours() / 24

	switch {
	case diff >= 0 && diff < 1:
		return fmt.Sprintf("Today — %s", date.Format("Monday, January 2"))
	case diff >= 1 && diff < 2:
		return fmt.Sprintf("Tomorrow — %s", date.Format("Monday, January 2"))
	case diff >= -1 && diff < 0:
		return fmt.Sprintf("Yesterday — %s", date.Format("Monday, January 2"))
	default:
		return date.Format("Monday, January 2, 2006")
	}
}
