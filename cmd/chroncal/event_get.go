package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func eventGetCmd() *cobra.Command {
	var recurrenceID string
	cmd := &cobra.Command{
		Use:   "get <id|uid>",
		Short: "Get event details by ID or UID",
		Long: `Show one event in detail.

You can look up the event by numeric ID or by its UID. Use
--recurrence-id to target a specific overridden instance from a
recurring series.`,
		Example: `  chroncal event get 42
  chroncal event get 6d7d8c3b-uid
  chroncal event get team-standup-uid --recurrence-id 2026-04-06T12:00:00Z --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			e, _, err := resolveEventOccurrence(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}

			populateEventFields(ctx, a.Events, &e)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONEvent(e))
			}
			printEvent(w, e)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}
