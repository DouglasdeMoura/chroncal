package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/tcal/internal/event"
)

type weekView struct {
	events map[string][]event.Event
	today  time.Time
}

func newWeekView() weekView {
	now := time.Now()
	return weekView{
		events: make(map[string][]event.Event),
		today:  time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local),
	}
}

func (w *weekView) setEvents(events []event.Event) {
	w.events = make(map[string][]event.Event)
	for _, e := range events {
		key := e.StartTime.Local().Format("2006-01-02")
		w.events[key] = append(w.events[key], e)
	}
}

func (w weekView) view(selected time.Time, width int) string {
	var b strings.Builder

	// Find Monday of the selected week
	weekday := selected.Weekday()
	offset := int(weekday) - 1
	if offset < 0 {
		offset = 6
	}
	monday := selected.AddDate(0, 0, -offset)

	// Header
	weekRange := fmt.Sprintf("%s – %s",
		monday.Format("Jan 2"),
		monday.AddDate(0, 0, 6).Format("Jan 2, 2006"))
	b.WriteString(titleStyle.Render(weekRange))
	b.WriteString("\n\n")

	// Column width
	colWidth := (width - 10) / 7
	if colWidth < 10 {
		colWidth = 10
	}

	// Day headers
	dayHeaderRow := lipgloss.NewStyle().Width(8).Render("")
	for i := 0; i < 7; i++ {
		day := monday.AddDate(0, 0, i)
		label := day.Format("Mon 02")

		style := lipgloss.NewStyle().
			Width(colWidth).
			Align(lipgloss.Center).
			Bold(true)

		if day.Equal(w.today) {
			style = style.Foreground(DefaultTheme.Today)
		} else if day.Equal(selected) {
			style = style.Foreground(DefaultTheme.Primary)
		} else {
			style = style.Foreground(DefaultTheme.TextDim)
		}

		dayHeaderRow += style.Render(label)
	}
	b.WriteString(dayHeaderRow)
	b.WriteString("\n")

	// Separator
	sep := lipgloss.NewStyle().Width(8).Render("")
	for i := 0; i < 7; i++ {
		sep += lipgloss.NewStyle().Width(colWidth).Foreground(DefaultTheme.Border).Render(strings.Repeat("─", colWidth-1))
	}
	b.WriteString(sep)
	b.WriteString("\n")

	// Hour rows (8:00 - 20:00)
	for hour := 8; hour <= 20; hour++ {
		timeLabel := lipgloss.NewStyle().
			Width(8).
			Foreground(DefaultTheme.TextDim).
			Render(fmt.Sprintf(" %02d:00", hour))

		row := timeLabel
		for i := 0; i < 7; i++ {
			day := monday.AddDate(0, 0, i)
			dayKey := day.Format("2006-01-02")
			cell := ""

			if evts, ok := w.events[dayKey]; ok {
				for _, e := range evts {
					if e.AllDay {
						continue
					}
					eHour := e.StartTime.Local().Hour()
					if eHour == hour {
						truncTitle := e.Title
						if len(truncTitle) > colWidth-3 {
							truncTitle = truncTitle[:colWidth-4] + "…"
						}
						cell = lipgloss.NewStyle().
							Foreground(DefaultTheme.Primary).
							Render("▐" + truncTitle)
					}
				}
			}

			style := lipgloss.NewStyle().Width(colWidth)
			if day.Equal(w.today) && hour == time.Now().Hour() {
				style = style.Background(DefaultTheme.Selected)
			}
			row += style.Render(cell)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}

	return b.String()
}
