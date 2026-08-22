package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func todoListCmd() *cobra.Command {
	var (
		calendarName   string
		status         string
		all            bool
		fromStr        string
		toStr          string
		compact        bool
		includeDeleted bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List todos (incomplete by default)",
		Long: `List todos in a date window.

By default completed and cancelled todos are hidden unless you pass
--all or filter explicitly with --status.`,
		Example: `  chroncal todo list
  chroncal todo list --all
  chroncal todo list --calendar Work --from 2026-04-01 --to 2026-04-30 --output json
  chroncal todo list --compact   # one line per todo (script-friendly)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			from, to, err := parseListDateRange(fromStr, toStr)
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

			todos, err := a.Recurrences.ListFilteredTodos(ctx, recurrence.TodoListParams{
				CalendarID:     calID,
				Status:         status,
				HideCompleted:  !all && status == "",
				From:           from,
				To:             to,
				IncludeDeleted: includeDeleted,
			})
			if err != nil {
				return fmt.Errorf("list todos: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodos(todos))
			}
			if compact {
				if len(todos) == 0 {
					fmt.Fprintln(w, "No todos found.")
					return nil
				}
				for _, t := range todos {
					fmt.Fprintln(w, formatCompactTodo(t))
				}
				return nil
			}
			printTodos(w, todos)
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
	cmd.Flags().BoolVar(&all, "all", false, "include completed and cancelled")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD); with no date flags, overdue todos are included")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 30 days after --from)")
	cmd.Flags().BoolVar(&compact, "compact", false, "one line per todo ([STATUS] DUE  TITLE)")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted todos (see `todo restore`)")
	return cmd
}

// formatCompactTodo renders one todo as a single line for scripts:
// "[x] 2026-05-25  Write report". The checkbox uses [x] when completed,
// [ ] otherwise. The date column is YYYY-MM-DD or "-" (no due date)
// padded to 12 chars so titles line up.
func formatCompactTodo(t todo.Todo) string {
	const dueColWidth = 12
	return fmt.Sprintf("%s %-*s%s", todoCheckbox(t), dueColWidth, compactDateColumn(t.DueDate), textsafe.Display(t.Summary))
}
