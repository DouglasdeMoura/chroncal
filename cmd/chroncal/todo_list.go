package main

import (
	"context"
	"fmt"
	"io"

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
		noHeader       bool
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
				writeCompactTodoTable(w, todos, !noHeader, compactTableColorEnabled(w))
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
	cmd.Flags().BoolVar(&compact, "compact", false, "table with one line per todo (ID  STATE  DUE  CATEGORIES  SUMMARY)")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "omit the compact table header (for scripts)")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted todos (see `todo restore`)")
	return cmd
}

// writeCompactTodoTable renders the todo compact table. The categories
// column disappears when no todo in the result set carries one, and a
// recurrence marker (\u21bb) flags recurring todos in the due column.
func writeCompactTodoTable(w io.Writer, todos []todo.Todo, showHeader, useColor bool) {
	termWidth := terminalWidth(w)
	headers := []string{"ID", "STATE", "DUE", "CATEGORIES", "SUMMARY"}
	rows := make([][]compactCell, len(todos))
	hasCategories := false
	for i, td := range todos {
		due := compactDateColumn(td.DueDate)
		if td.RecurrenceRule != "" || td.RecurrenceID != "" {
			due += " \u21bb"
		}
		categories := textsafe.Display(td.Categories)
		if categories == "" {
			categories = "-"
		} else {
			hasCategories = true
		}
		stateColor := "2"
		if td.IsCompleted() {
			stateColor = "32"
		}
		rows[i] = []compactCell{
			{fmt.Sprintf("%d", td.ID), "1;36"},
			{todoCheckbox(td), stateColor},
			{due, "2"},
			{categories, "33"},
			{textsafe.Display(td.Summary), ""},
		}
	}
	flex := map[int]bool{3: true, 4: true}
	if !hasCategories {
		headers = dropCompactColumn(headers, 3)
		flex = remapFlex(flex, 3)
		for i := range rows {
			rows[i] = dropCompactCell(rows[i], 3)
		}
	}
	writeCompactTable(w, headers, rows, flex, useColor, showHeader, termWidth)
}
