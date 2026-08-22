package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func todoGetCmd() *cobra.Command {
	var recurrenceID string
	cmd := &cobra.Command{
		Use:   "get <id|uid>",
		Short: "Get todo details by ID or UID",
		Long: `Show one todo in detail.

You can look it up by numeric ID or UID. Use --recurrence-id to target a
specific overridden instance from a recurring series.`,
		Example: `  chroncal todo get 7
  chroncal todo get weekly-review-uid
  chroncal todo get weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			t, err := resolveTodo(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			populateTodoFields(ctx, a.Todos, &t)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodo(t))
			}
			printTodo(w, t)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}
