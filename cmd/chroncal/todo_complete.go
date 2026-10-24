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
its progress to 100%.

With --recurrence-id and the UID of a recurring todo, the command completes
one occurrence. When the occurrence has no override yet, the command creates
one. The other occurrences do not change.`,
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

			t, createOverride, err := resolveTodoOccurrence(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			if createOverride {
				// The occurrence has no row yet. The override takes the
				// fields of the occurrence, completed (issue #887).
				p := todoParamsOf(t)
				p.Status, p.CompletedAt = "COMPLETED", ""
				t, err = a.Todos.CreateOverride(ctx, t.UID, t.RecurrenceID, p)
			} else {
				t, err = a.Todos.Complete(ctx, t.ID)
			}
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
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target one occurrence of a recurring todo (RFC 3339 timestamp)")
	return cmd
}
