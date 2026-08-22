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
  chroncal event list --compact   # one line per event (script-friendly)`,
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
				for _, e := range events {
					fmt.Fprintln(w, formatCompactEvent(e, calendarNames, showID, showCalendar))
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
	cmd.Flags().BoolVar(&compact, "compact", false, "one line per event (DATE  TIME  TITLE); skips empty-day stubs")
	cmd.Flags().BoolVar(&showID, "show-id", false, "show each event's numeric ID in text output")
	cmd.Flags().BoolVar(&showCalendar, "show-calendar", false, "show the calendar name in text output")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted events (see `events restore`)")
	mutuallyExclusive(cmd, "compact", "verbose")
	return cmd
}

// formatCompactEvent renders one event as three whitespace-separated columns
// for line-per-event scripts: date(-range), time(-range or all-day),
// and title. The columns are padded to fixed widths so an awk/cut user can
// extract them by position. The title still contains arbitrary text. Read it
// as "rest of line". Date ranges use ISO 8601 interval syntax
// (start/end). --show-id prefixes "[id]". --show-calendar suffixes "(name)".
func formatCompactEvent(e event.Event, calendarNames map[int64]string, showID, showCalendar bool) string {
	const (
		dateColWidth = 23 // "YYYY-MM-DD/YYYY-MM-DD" + 2 trailing spaces
		timeColWidth = 13 // "HH:MM-HH:MM" + 2 trailing spaces
	)
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
	var b strings.Builder
	if showID {
		fmt.Fprintf(&b, "[%d]  ", e.ID)
	}
	fmt.Fprintf(&b, "%-*s%-*s%s", dateColWidth, dateCol, timeColWidth, timeCol, textsafe.Display(e.Title))
	if showCalendar {
		if name, ok := calendarNames[e.CalendarID]; ok && name != "" {
			fmt.Fprintf(&b, "  (%s)", textsafe.Display(name))
		}
	}
	return b.String()
}
