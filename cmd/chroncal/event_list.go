package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
	"github.com/douglasdemoura/chroncal/internal/tui"
)

func eventListCmd() *cobra.Command {
	var (
		fromStr        string
		toStr          string
		calendarName   string
		status         string
		showWeekday    bool
		verbose        bool
		compact        bool
		showID         bool
		showCalendar   bool
		includeDeleted bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List events in a date range",
		Long: `List events in a date range, expanding recurring series into the
instances that fall inside the requested window.

Without flags, the window defaults to today through the next 30 days.`,
		Example: `  chroncal event list
  chroncal event list --calendar Work --from 2026-04-01 --to 2026-04-07
  chroncal event list --status CONFIRMED --output json
  chroncal event list --compact   # table with ID, date, time, categories, and summary`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			from, to, err := parseDateRange(fromStr, toStr)
			if err != nil {
				return err
			}

			var calID int64
			if calendarName != "" {
				calID, err = resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
			}

			events, err := a.Recurrences.ListFilteredEvents(ctx, recurrence.EventListParams{
				CalendarID:     calID,
				Status:         status,
				From:           from,
				To:             to,
				IncludeDeleted: includeDeleted,
			})
			if err != nil {
				return fmt.Errorf("list events: %w", err)
			}

			var calendarNames map[int64]string
			if verbose || showCalendar || compact {
				cals, err := a.Calendars.List(ctx)
				if err != nil {
					return fmt.Errorf("list calendars: %w", err)
				}
				calendarNames = make(map[int64]string, len(cals))
				for _, cal := range cals {
					calendarNames[cal.ID] = cal.Name
				}
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				items := make([]jsonEvent, len(events))
				for i, e := range events {
					items[i] = toJSONEvent(e)
				}
				return printOutput(w, items)
			}
			if len(events) == 0 {
				fmt.Fprintln(w, "No events found.")
				return nil
			}
			if compact {
				useColor := compactTableColorEnabled(w)
				fmt.Fprintln(w, formatCompactEventHeader(showCalendar, useColor))
				for _, e := range events {
					fmt.Fprintln(w, formatCompactEvent(e, calendarNames, showCalendar, useColor))
				}
				return nil
			}
			// ShowAllDays:false suppresses date-only stub lines for days
			// with no events. Days with events still render normally.
			fmt.Fprint(w, tui.FormatEventList(tui.FormatEventListOptions{
				Events:        events,
				CalendarNames: calendarNames,
				ShowHeader:    false,
				ShowAllDays:   false,
				ShowWeekday:   showWeekday,
				ShowMonth:     true,
				Verbose:       verbose,
				ShowID:        showID,
				ShowCalendar:  showCalendar,
				From:          from,
				To:            to,
			}))
			return nil
		},
	}
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 30 days from now)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (TENTATIVE, CONFIRMED, CANCELLED)")
	cmd.Flags().BoolVar(&showWeekday, "show-weekday", false, "show weekday abbreviation next to the date")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "render a detailed time-rail view for each event")
	cmd.Flags().BoolVar(&compact, "compact", false, "table with one line per event (ID  DATE  TIME  CATEGORIES  SUMMARY); skips empty-day stubs")
	cmd.Flags().BoolVar(&showID, "show-id", false, "show each event's numeric ID in non-compact text output (compact always includes it)")
	cmd.Flags().BoolVar(&showCalendar, "show-calendar", false, "show the calendar name in text output")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted events (see `events restore`)")
	mutuallyExclusive(cmd, "compact", "verbose")
	return cmd
}

const (
	compactEventIDWidth         = 6
	compactEventDateWidth       = 23 // "YYYY-MM-DD/YYYY-MM-DD" + 2 trailing spaces
	compactEventTimeWidth       = 13 // "HH:MM-HH:MM" + 2 trailing spaces
	compactEventCategoriesWidth = 20
	compactEventCalendarWidth   = 18
)

func formatCompactEventHeader(showCalendar, useColor bool) string {
	header := fmt.Sprintf("%-*s%-*s%-*s%-*s",
		compactEventIDWidth, "ID",
		compactEventDateWidth, "DATE",
		compactEventTimeWidth, "TIME",
		compactEventCategoriesWidth, "CATEGORIES")
	if showCalendar {
		header += fmt.Sprintf("%-*s", compactEventCalendarWidth, "CALENDAR")
	}
	header += "SUMMARY"
	return compactTableColor(useColor, "1;36", header)
}

// formatCompactEvent renders one event as a table row. Date ranges use ISO
// 8601 interval syntax (start/end); summary remains last so arbitrary text
// does not disturb the preceding columns.
func formatCompactEvent(e event.Event, calendarNames map[int64]string, showCalendar, useColor bool) string {
	start := e.StartTime.Local()
	end := e.EndTime.Local()
	var dateCol, timeCol string
	if e.AllDay {
		last := end.AddDate(0, 0, -1)
		if start.Year() == last.Year() && start.YearDay() == last.YearDay() {
			dateCol = start.Format("2006-01-02")
		} else {
			dateCol = start.Format("2006-01-02") + "/" + last.Format("2006-01-02")
		}
		timeCol = "all-day"
	} else if start.Year() == end.Year() && start.YearDay() == end.YearDay() {
		dateCol = start.Format("2006-01-02")
		timeCol = start.Format("15:04") + "-" + end.Format("15:04")
	} else {
		dateCol = start.Format("2006-01-02") + "/" + end.Format("2006-01-02")
		timeCol = start.Format("15:04") + "-" + end.Format("15:04")
	}
	categories := textsafe.Display(e.Categories)
	if categories == "" {
		categories = "-"
	}

	var b strings.Builder
	b.WriteString(compactTableColor(useColor, "1;36", fmt.Sprintf("%-*d", compactEventIDWidth, e.ID)))
	b.WriteString(compactTableColor(useColor, "2", fmt.Sprintf("%-*s", compactEventDateWidth, dateCol)))
	b.WriteString(compactTableColor(useColor, "2", fmt.Sprintf("%-*s", compactEventTimeWidth, timeCol)))
	b.WriteString(compactTableColor(useColor, "33", fmt.Sprintf("%-*s", compactEventCategoriesWidth, categories)))
	if showCalendar {
		name := "-"
		if calendarName, ok := calendarNames[e.CalendarID]; ok && calendarName != "" {
			name = textsafe.Display(calendarName)
		}
		b.WriteString(compactTableColor(useColor, "35", fmt.Sprintf("%-*s", compactEventCalendarWidth, name)))
	}
	b.WriteString(textsafe.Display(e.Title))
	return b.String()
}
