package tui

import (
	"fmt"
	"sort"
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
		return ev.StartTime.Local().Format("15:04") + "-" + ev.EndTime.Local().Format("15:04")
	}
}

// formatTimeColumnMulti returns the time-column label for an event on a
// specific day within its span. Start day shows "HH:MM→     ", last day
// shows "     →HH:MM", middle days are blank. All-day and single-day
// events fall back to formatTimeColumn.
func formatTimeColumnMulti(ev event.Event, dayIndex, totalDays int) string {
	if ev.AllDay || totalDays <= 1 {
		return formatTimeColumn(ev)
	}
	switch dayIndex {
	case 1:
		return ev.StartTime.Local().Format("15:04") + "→     "
	case totalDays:
		return "     →" + ev.EndTime.Local().Format("15:04")
	default:
		return "           "
	}
}

// effectiveStartOnDay returns the event's start time as experienced on a
// given day. That is the original start for its first day. It is midnight
// of that day for any continuation day. Used to sort multi-day events
// (which stay live from 00:00 on continuation days) above single-day events.
func effectiveStartOnDay(ev event.Event, day time.Time, dayIndex int) time.Time {
	if dayIndex == 1 {
		return ev.StartTime
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
}

// spanDays returns the local calendar days an event touches. The end time
// is treated as exclusive. An event that ends exactly at midnight then does
// not count the following day. All-day events are stored as midnight-UTC
// datestamps. Their span is derived from the UTC date, anchored at local
// midnight to match the timed-event convention used by callers' window and
// boundary comparisons. They then land on the correct day regardless of the
// local timezone offset (mirrors eventCalendarDays).
func spanDays(ev event.Event) []time.Time {
	if ev.AllDay {
		s := ev.StartTime.UTC()
		e := ev.EndTime.UTC()
		startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.Local)
		var days []time.Time
		for d := s; d.Before(e); d = d.AddDate(0, 0, 1) {
			days = append(days, time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local))
		}
		if len(days) == 0 {
			days = []time.Time{startDay}
		}
		return days
	}
	s := ev.StartTime.Local()
	startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, s.Location())
	e := ev.EndTime.Local()
	if !e.After(s) {
		return []time.Time{startDay}
	}
	var days []time.Time
	for d := startDay; d.Before(e); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	if len(days) == 0 {
		days = []time.Time{startDay}
	}
	return days
}

type FormatEventListOptions struct {
	Events        []event.Event
	CalendarNames map[int64]string
	ShowHeader    bool
	ShowAllDays   bool
	From          time.Time
	To            time.Time
	// WeekdayWidth controls the weekday label width (1, 2, or 3 chars).
	// Zero or out-of-range values default to 3.
	WeekdayWidth int
	// ShowWeekday controls whether the weekday label is displayed.
	ShowWeekday bool
	// ShowMonth controls whether the month label is displayed in the day prefix.
	ShowMonth bool
	// Verbose renders a richer time-rail view and suppresses empty days.
	Verbose bool
	// ShowID appends the event ID in text output.
	ShowID bool
	// ShowCalendar appends or prints the calendar name in text output.
	ShowCalendar bool
}

type eventListDayEntry struct {
	ev        event.Event
	dayIndex  int
	totalDays int
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

	eventsByDay := make(map[string][]eventListDayEntry)
	for _, ev := range opts.Events {
		days := spanDays(ev)
		total := len(days)
		for i, d := range days {
			key := d.Format("2006-01-02")
			eventsByDay[key] = append(eventsByDay[key], eventListDayEntry{
				ev:        ev,
				dayIndex:  i + 1,
				totalDays: total,
			})
		}
	}

	// Sort each day's entries so continuations of earlier events (which are
	// active from the start of the day) appear before events starting later.
	for k, entries := range eventsByDay {
		day, _ := time.ParseInLocation("2006-01-02", k, time.Local)
		sort.SliceStable(entries, func(a, b int) bool {
			return effectiveStartOnDay(entries[a].ev, day, entries[a].dayIndex).
				Before(effectiveStartOnDay(entries[b].ev, day, entries[b].dayIndex))
		})
		eventsByDay[k] = entries
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
			for _, d := range spanDays(ev) {
				dayKey := d.Format("2006-01-02")
				if !seen[dayKey] {
					seen[dayKey] = true
					addDay(d)
				}
			}
		}
	}

	var out strings.Builder
	firstVerboseDay := true
	for _, monthKey := range monthOrder {
		if opts.ShowHeader {
			t, _ := time.Parse("2006-01", monthKey)
			out.WriteString(lipgloss.NewStyle().Bold(true).Render(t.Format("January 2006")))
			out.WriteString("\n\n")
		}

		for _, dayKey := range months[monthKey] {
			dayEvents := eventsByDay[dayKey]
			d, _ := time.Parse("2006-01-02", dayKey)
			dayPrefix := d.Format("02")
			if opts.ShowWeekday {
				dayPrefix += " " + formatWeekday(d, weekdayWidth)
			}
			if opts.ShowMonth {
				dayPrefix = d.Format("Jan") + " " + dayPrefix
			}

			if opts.Verbose {
				if len(dayEvents) == 0 {
					continue
				}
				if !firstVerboseDay {
					out.WriteByte('\n')
				}
				out.WriteString(dayPrefix)
				out.WriteByte('\n')
				out.WriteString(strings.Repeat("-", len(dayPrefix)))
				out.WriteByte('\n')
				for _, entry := range dayEvents {
					writeVerboseEventListEntry(&out, entry, opts)
				}
				firstVerboseDay = false
				continue
			}

			if len(dayEvents) == 0 {
				out.WriteString(dayPrefix)
				out.WriteByte('\n')
				continue
			}

			continuation := strings.Repeat(" ", len(dayPrefix))
			for i, entry := range dayEvents {
				if i == 0 {
					out.WriteString(dayPrefix)
				} else {
					out.WriteString(continuation)
				}
				out.WriteString(" ")
				out.WriteString(formatTimeColumnMulti(entry.ev, entry.dayIndex, entry.totalDays))
				out.WriteString("  ")
				out.WriteString(compactEventLabel(entry, opts))
				out.WriteByte('\n')
			}
		}

		if opts.ShowHeader {
			out.WriteByte('\n')
		}
	}

	return out.String()
}

func verboseTimeRailLabel(ev event.Event, dayIndex int) string {
	switch {
	case ev.AllDay:
		return "all day"
	case dayIndex == 1:
		return ev.StartTime.Local().Format("15:04")
	default:
		return "00:00"
	}
}

func verboseContinuationLabel(ev event.Event, dayIndex, totalDays int) string {
	if totalDays <= 1 || ev.AllDay {
		return ""
	}
	switch dayIndex {
	case 1:
		return "ends " + ev.EndTime.Local().Format("Mon, Jan 2 15:04")
	case totalDays:
		return "until " + ev.EndTime.Local().Format("15:04")
	default:
		return "continues"
	}
}

func compactEventLabel(entry eventListDayEntry, opts FormatEventListOptions) string {
	label := entry.ev.Title
	if opts.ShowID && entry.ev.ID > 0 {
		label += fmt.Sprintf(" (%d)", entry.ev.ID)
	}
	if entry.totalDays > 1 {
		label += fmt.Sprintf(" (day %d/%d)", entry.dayIndex, entry.totalDays)
	}
	if opts.ShowCalendar {
		if calendarLabel := formatCalendarLabel(entry.ev.CalendarID, opts.CalendarNames); calendarLabel != "" {
			label += " [" + calendarLabel + "]"
		}
	}
	return label
}

func writeVerboseEventListEntry(out *strings.Builder, entry eventListDayEntry, opts FormatEventListOptions) {
	title := entry.ev.Title
	if opts.ShowID && entry.ev.ID > 0 {
		title = fmt.Sprintf("%s (%d)", title, entry.ev.ID)
	}
	if entry.totalDays > 1 {
		title = fmt.Sprintf("%s (day %d/%d)", title, entry.dayIndex, entry.totalDays)
	}

	fmt.Fprintf(out, "%-7s | %s\n", verboseTimeRailLabel(entry.ev, entry.dayIndex), title)
	if entry.ev.Location != "" {
		fmt.Fprintf(out, "%7s | %s\n", "", entry.ev.Location)
	}
	if entry.ev.Description != "" {
		fmt.Fprintf(out, "%7s | %s\n", "", entry.ev.Description)
	}
	if metadata := verboseMetadataLine(entry.ev, opts); metadata != "" {
		fmt.Fprintf(out, "%7s | %s\n", "", metadata)
	}
	if continuation := verboseContinuationLabel(entry.ev, entry.dayIndex, entry.totalDays); continuation != "" {
		fmt.Fprintf(out, "%7s | %s\n", "", continuation)
	}
}

func formatCalendarLabel(calendarID int64, calendarNames map[int64]string) string {
	if calendarID <= 0 {
		return ""
	}
	if name := calendarNames[calendarID]; name != "" {
		return name
	}
	return fmt.Sprintf("%d", calendarID)
}

func verboseMetadataLine(ev event.Event, opts FormatEventListOptions) string {
	if !opts.ShowCalendar {
		return ""
	}
	if calendarLabel := formatCalendarLabel(ev.CalendarID, opts.CalendarNames); calendarLabel != "" {
		return "Calendar: " + calendarLabel
	}
	return ""
}
