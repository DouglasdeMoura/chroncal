package tui

import (
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

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
			if !opts.ShowHeader {
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
