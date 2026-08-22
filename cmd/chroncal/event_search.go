package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/tui"
)

func eventSearchCmd() *cobra.Command {
	var (
		calendarName string
		fromStr      string
		toStr        string
		status       string
		compact      bool
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search events by title, description, location, or categories",
		Long: `Search events by text fields such as title, description, location,
and categories.

Use --from and --to to narrow the search window when you already know
roughly when the event occurred.`,
		Example: `  chroncal event search standup
  chroncal event search deploy --calendar Work --status CONFIRMED
  chroncal event search conference --from 2026-04-01 --to 2026-05-01
  chroncal event search standup --compact`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			var calID int64
			if calendarName != "" {
				calID, err = resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
			}

			// Parse the YYYY-MM-DD --from/--to flags into RFC3339 UTC bounds
			// before populating SearchParams. Search compares these strings
			// lexicographically against UTC-stored start times, so feeding the
			// raw "2026-04-30" through would make "2026-04-30T09:00:00Z" sort
			// after the bound (since 'T' > '0') and silently drop every event
			// on the end day (issue #428). parseExportDateBounds (not
			// parseDateRange) treats each bound independently so a one-sided
			// window stays open on the other side, and its half-open --to
			// includes the entire end day.
			fromT, toT, err := parseExportDateBounds(fromStr, toStr)
			if err != nil {
				return err
			}
			var from, to string
			if !fromT.IsZero() {
				from = fromT.UTC().Format(time.RFC3339)
			}
			if !toT.IsZero() {
				to = toT.UTC().Format(time.RFC3339)
			}

			events, err := a.Events.Search(ctx, event.SearchParams{
				Query:      args[0],
				CalendarID: calID,
				From:       from,
				To:         to,
				Status:     status,
			})
			if err != nil {
				return fmt.Errorf("search events: %w", err)
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
					fmt.Fprintln(w, formatCompactEvent(e, nil, false, false))
				}
				return nil
			}
			fmt.Fprint(w, tui.FormatEventList(tui.FormatEventListOptions{
				Events:      events,
				ShowAllDays: false,
				ShowMonth:   true,
			}))
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date filter (YYYY-MM-DD)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date filter (YYYY-MM-DD, inclusive)")
	cmd.Flags().StringVar(&status, "status", "", "status filter (TENTATIVE, CONFIRMED, CANCELLED)")
	cmd.Flags().BoolVar(&compact, "compact", false, "one line per event (DATE  TIME  TITLE); same shape as event list --compact")
	return cmd
}
