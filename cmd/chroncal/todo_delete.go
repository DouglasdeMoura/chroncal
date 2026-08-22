package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func todoDeleteCmd() *cobra.Command {
	var (
		recurrenceID string
		series       bool
	)
	cmd := &cobra.Command{
		Use:   "delete <id|uid>",
		Short: "Delete a todo",
		Long: `Delete a single todo, a specific recurring override, or an entire
recurring series.`,
		Example: `  chroncal todo delete 7
  chroncal todo delete weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z
  chroncal todo delete weekly-review-uid --series`,
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

			if series && recurrenceID != "" {
				return errInvalidInputf("--series and --recurrence-id are mutually exclusive")
			}

			question := fmt.Sprintf("Delete todo %q?", safeText(t.Summary))
			if series {
				question = fmt.Sprintf("Delete the entire recurring series %q (master + all overrides)?", safeText(t.Summary))
			} else if recurrenceID != "" {
				question = fmt.Sprintf("Delete override instance of %q at %s?", safeText(t.Summary), recurrenceID)
			}
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if series {
				if err := a.Todos.DeleteSeries(ctx, t.UID); err != nil {
					return fmt.Errorf("delete series: %w", err)
				}
				w := cmd.OutOrStdout()
				if outputFmt != "text" {
					if err := printOutput(w, map[string]any{"deleted": true, "uid": t.UID, "series": true}); err != nil {
						return err
					}
					opportunisticPush(a, t.CalendarID, cmd)
					return nil
				}
				fmt.Fprintf(w, "Deleted todo series %q.\n", safeText(t.UID))
				opportunisticPush(a, t.CalendarID, cmd)
				return nil
			}

			if err := a.Todos.Delete(ctx, t.ID); err != nil {
				return fmt.Errorf("delete todo: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, map[string]any{"deleted": true, "id": t.ID}); err != nil {
					return err
				}
				opportunisticPush(a, t.CalendarID, cmd)
				return nil
			}
			fmt.Fprintf(w, "Deleted todo %d.\n", t.ID)
			opportunisticPush(a, t.CalendarID, cmd)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	cmd.Flags().BoolVar(&series, "series", false, "delete the entire recurring series (master + all overrides)")
	addConfirmFlag(cmd)
	return cmd
}
