package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/todo"
)

func todoSearchCmd() *cobra.Command {
	var (
		calendarName string
		status       string
		completed    bool
		incomplete   bool
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search todos by summary, description, location, or categories",
		Long: `Search todos by text fields such as summary, description, location,
and categories.`,
		Example: `  chroncal todo search release
  chroncal todo search invoice --completed
  chroncal todo search onboarding --calendar Work --status IN-PROCESS`,
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

			completedFilter := 0
			if completed {
				completedFilter = 1
			} else if incomplete {
				completedFilter = 2
			}

			todos, err := a.Todos.Search(ctx, todo.SearchParams{
				Query:      args[0],
				CalendarID: calID,
				Status:     status,
				Completed:  completedFilter,
			})
			if err != nil {
				return fmt.Errorf("search todos: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodos(todos))
			}
			printTodos(w, todos)
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "status filter (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
	cmd.Flags().BoolVar(&completed, "completed", false, "show only completed todos")
	cmd.Flags().BoolVar(&incomplete, "incomplete", false, "show only incomplete todos")
	mutuallyExclusive(cmd, "completed", "incomplete")
	return cmd
}
