package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func todoCompleteCmd() *cobra.Command {
	var recurrenceID string
	cmd := &cobra.Command{
		Use:   "complete <id|uid>",
		Short: "Mark a todo as completed",
		Long: `Mark a todo as completed, set its completion timestamp, and update
its progress to 100%.`,
		Example: `  chroncal todo complete 7
  chroncal todo complete weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z`,
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

			t, err = a.Todos.Complete(ctx, t.ID)
			if err != nil {
				return fmt.Errorf("complete todo: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, toJSONTodo(t)); err != nil {
					return err
				}
				opportunisticPush(a, t.CalendarID, cmd)
				return nil
			}
			fmt.Fprintf(w, "Completed: %s\n", safeText(t.Summary))
			opportunisticPush(a, t.CalendarID, cmd)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}
