package main

import (
	"context"
	"fmt"
	"io"

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
		noHeader       bool
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
				writeCompactEventTable(w, events, calendarNames, showCalendar, !noHeader, compactTableColorEnabled(w))
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
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "omit the compact table header (for scripts)")
	cmd.Flags().BoolVar(&showCalendar, "show-calendar", false, "show the calendar name in text output")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted events (see `events restore`)")
	mutuallyExclusive(cmd, "compact", "verbose")
	return cmd
}

// writeCompactEventTable renders the event compact table: one header row and
// one row per event. The categories column disappears when no event in the
// result set carries one, and a recurrence marker (\u21bb) flags series and
// moved overrides in the date column.
func writeCompactEventTable(w io.Writer, events []event.Event, calendarNames map[int64]string, showCalendar, showHeader, useColor bool) {
	termWidth := terminalWidth(w)
	headers := []string{"ID", "DATE", "TIME", "CATEGORIES", "SUMMARY"}
	codes := []string{"1;36", "2", "2", "33", ""}
	if showCalendar {
		headers = append(headers[:4], append([]string{"CALENDAR"}, headers[4:]...)...)
		codes = append(codes[:4], append([]string{"35"}, codes[4:]...)...)
	}
	rows := make([][]compactCell, len(events))
	hasCategories := false
	for i, e := range events {
		dateCol, timeCol := compactEventDateColumns(e)
		if e.RecurrenceRule != "" || e.RecurrenceID != "" {
			dateCol += " \u21bb"
		}
		categories := textsafe.Display(e.Categories)
		if categories == "" {
			categories = "-"
		} else {
			hasCategories = true
		}
		row := []compactCell{
			{fmt.Sprintf("%d", e.ID), "1;36"},
			{dateCol, "2"},
			{timeCol, "2"},
			{categories, "33"},
			{textsafe.Display(e.Title), ""},
		}
		if showCalendar {
			name := "-"
			if calendarName, ok := calendarNames[e.CalendarID]; ok && calendarName != "" {
				name = textsafe.Display(calendarName)
			}
			row = append(row[:4], append([]compactCell{{name, "35"}}, row[4:]...)...)
		}
		rows[i] = row
	}
	flex := map[int]bool{3: true, len(headers) - 1: true}
	if showCalendar {
		flex[4] = true
	}
	if !hasCategories {
		headers = dropCompactColumn(headers, 3)
		codes = dropCompactColumn(codes, 3)
		flex = remapFlex(flex, 3, len(headers))
		for i := range rows {
			rows[i] = dropCompactCell(rows[i], 3)
		}
	}
	writeCompactTable(w, headers, codes, rows, flex, useColor, showHeader, termWidth)
}

// compactEventDateColumns formats the date and time columns for one event.
// Date ranges use ISO 8601 interval syntax (start/end).
func compactEventDateColumns(e event.Event) (dateCol, timeCol string) {
	start := e.StartTime.Local()
	end := e.EndTime.Local()
	if e.AllDay {
		last := end.AddDate(0, 0, -1)
		if start.Year() == last.Year() && start.YearDay() == last.YearDay() {
			dateCol = start.Format("2006-01-02")
		} else {
			dateCol = start.Format("2006-01-02") + "/" + last.Format("2006-01-02")
		}
		return dateCol, "all-day"
	}
	if start.Year() == end.Year() && start.YearDay() == end.YearDay() {
		return start.Format("2006-01-02"), start.Format("15:04") + "-" + end.Format("15:04")
	}
	return start.Format("2006-01-02") + "/" + end.Format("2006-01-02"),
		start.Format("15:04") + "-" + end.Format("15:04")
}
