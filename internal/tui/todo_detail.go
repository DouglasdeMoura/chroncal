package tui

import (
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/todo"
)

func renderTodoDetail(t *todo.Todo, calendarName string) string {
	if t == nil {
		return ""
	}

	var b strings.Builder

	b.WriteString(titleStyle.Render(t.Summary))
	b.WriteString("\n\n")

	// Status
	statusLabel := t.Status
	if t.PercentComplete > 0 && t.PercentComplete < 100 {
		statusLabel = fmt.Sprintf("%s (%d%%)", t.Status, t.PercentComplete)
	}
	row := eventDetailLabelStyle.Render("Status") + eventDetailValueStyle.Render(statusLabel)
	b.WriteString(row + "\n")

	// Due date
	if t.DueDate != "" {
		due := t.ParseDueDate().Local()
		dueStr := due.Format("Monday, January 2, 2006")
		if t.IsOverdue() {
			dueStr += " (OVERDUE)"
		}
		row := eventDetailLabelStyle.Render("Due") + eventDetailValueStyle.Render(dueStr)
		b.WriteString(row + "\n")
	}

	if t.Location != "" {
		row := eventDetailLabelStyle.Render("Where") + eventDetailValueStyle.Render(t.Location)
		b.WriteString(row + "\n")
	}

	if calendarName != "" {
		row := eventDetailLabelStyle.Render("Calendar") + eventDetailValueStyle.Render(calendarName)
		b.WriteString(row + "\n")
	}

	if t.Priority > 0 {
		row := eventDetailLabelStyle.Render("Priority") + eventDetailValueStyle.Render(fmt.Sprintf("%d", t.Priority))
		b.WriteString(row + "\n")
	}

	if t.URL != "" {
		row := eventDetailLabelStyle.Render("URL") + eventDetailValueStyle.Render(t.URL)
		b.WriteString(row + "\n")
	}

	if t.Categories != "" {
		row := eventDetailLabelStyle.Render("Tags") + eventDetailValueStyle.Render(t.Categories)
		b.WriteString(row + "\n")
	}

	if len(t.Alarms) > 0 {
		row := eventDetailLabelStyle.Render("Reminders") + eventDetailValueStyle.Render(fmt.Sprintf("%d reminder(s)", len(t.Alarms)))
		b.WriteString(row + "\n")
	}

	if t.Description != "" {
		b.WriteString("\n")
		b.WriteString(eventDetailValueStyle.Render(t.Description))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if !t.IsCompleted() {
		b.WriteString(helpKeyStyle.Render("space") + helpDescStyle.Render(" complete  "))
	}
	b.WriteString(helpKeyStyle.Render("e") + helpDescStyle.Render(" edit  "))
	b.WriteString(helpKeyStyle.Render("d") + helpDescStyle.Render(" delete  "))
	b.WriteString(helpKeyStyle.Render("esc") + helpDescStyle.Render(" back"))

	return b.String()
}
