package tui

import (
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func renderEventDetail(e *event.Event, calendarName string) string {
	if e == nil {
		return ""
	}

	var b strings.Builder

	b.WriteString(titleStyle.Render(e.Title))
	b.WriteString("\n\n")

	// Time
	if e.AllDay {
		row := eventDetailLabelStyle.Render("When") +
			eventDetailValueStyle.Render(e.StartTime.Local().Format("Monday, January 2, 2006")+" (all day)")
		b.WriteString(row + "\n")
	} else {
		timeStr := fmt.Sprintf("%s – %s",
			e.StartTime.Local().Format("Mon, Jan 2 15:04"),
			e.EndTime.Local().Format("15:04"))
		row := eventDetailLabelStyle.Render("When") + eventDetailValueStyle.Render(timeStr)
		b.WriteString(row + "\n")
	}

	if e.Location != "" {
		row := eventDetailLabelStyle.Render("Where") + eventDetailValueStyle.Render(e.Location)
		b.WriteString(row + "\n")
	}

	if calendarName != "" {
		row := eventDetailLabelStyle.Render("Calendar") + eventDetailValueStyle.Render(calendarName)
		b.WriteString(row + "\n")
	}

	if e.Status != "" && e.Status != "CONFIRMED" {
		row := eventDetailLabelStyle.Render("Status") + eventDetailValueStyle.Render(e.Status)
		b.WriteString(row + "\n")
	}

	if e.URL != "" {
		row := eventDetailLabelStyle.Render("URL") + eventDetailValueStyle.Render(e.URL)
		b.WriteString(row + "\n")
	}

	if e.Categories != "" {
		row := eventDetailLabelStyle.Render("Tags") + eventDetailValueStyle.Render(e.Categories)
		b.WriteString(row + "\n")
	}

	if len(e.Alarms) > 0 {
		label := fmt.Sprintf("%d reminder(s)", len(e.Alarms))
		row := eventDetailLabelStyle.Render("Reminders") + eventDetailValueStyle.Render(label)
		b.WriteString(row + "\n")
	}

	if len(e.Attendees) > 0 {
		label := fmt.Sprintf("%d participant(s)", len(e.Attendees))
		row := eventDetailLabelStyle.Render("Attendees") + eventDetailValueStyle.Render(label)
		b.WriteString(row + "\n")
	}

	if e.Description != "" {
		b.WriteString("\n")
		b.WriteString(eventDetailValueStyle.Render(e.Description))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpKeyStyle.Render("e") + helpDescStyle.Render(" edit  "))
	b.WriteString(helpKeyStyle.Render("d") + helpDescStyle.Render(" delete  "))
	b.WriteString(helpKeyStyle.Render("esc") + helpDescStyle.Render(" back"))

	return b.String()
}
