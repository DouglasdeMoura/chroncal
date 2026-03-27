package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/todo"
)

func todoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "todo",
		Short: "Manage todos",
	}
	cmd.AddCommand(todoListCmd(), todoGetCmd(), todoAddCmd(), todoUpdateCmd(), todoDeleteCmd())
	return cmd
}

func todoListCmd() *cobra.Command {
	var (
		calendarName string
		status       string
		all          bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List todos (incomplete by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			var todos []todo.Todo
			switch {
			case all:
				todos, err = a.Todos.ListAll(ctx)
			case status != "":
				todos, err = a.Todos.ListByStatus(ctx, status)
			case calendarName != "":
				calID, cerr := resolveCalendarID(ctx, a, calendarName)
				if cerr != nil {
					return cerr
				}
				todos, err = a.Todos.ListByCalendar(ctx, calID)
			default:
				todos, err = a.Todos.List(ctx)
			}
			if err != nil {
				return fmt.Errorf("list todos: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, toJSONTodos(todos))
			}
			printTodos(w, todos)
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
	cmd.Flags().BoolVar(&all, "all", false, "include completed and cancelled")
	return cmd
}

func todoGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get todo details by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid todo ID: %w", err)
			}

			t, err := a.Todos.Get(ctx, id)
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			t.Alarms, _ = a.Todos.ListAlarms(ctx, t.ID)
			t.Attendees, _ = a.Todos.ListAttendees(ctx, t.ID)
			t.Attachments, _ = a.Todos.ListAttachments(ctx, t.ID)
			t.Comments, _ = a.Todos.ListComments(ctx, t.ID)
			t.Contacts, _ = a.Todos.ListContacts(ctx, t.ID)
			t.Resources, _ = a.Todos.ListResources(ctx, t.ID)
			t.Relations, _ = a.Todos.ListRelations(ctx, t.ID)

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, toJSONTodo(t))
			}
			printTodo(w, t)
			return nil
		},
	}
	return cmd
}

func todoAddCmd() *cobra.Command {
	var (
		dueStr       string
		calendarName string
		location     string
		description  string
		priority     int64
		categories   string
		url          string
		attachFlags  []string
	)
	cmd := &cobra.Command{
		Use:   `add "<summary>"`,
		Short: "Create a new todo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			calID, err := resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}

			var dueDate string
			if dueStr != "" {
				d, err := time.ParseInLocation("2006-01-02", dueStr, time.Local)
				if err != nil {
					return fmt.Errorf("parse due date: %w", err)
				}
				// Due at end of day
				dueDate = time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, time.Local).Format(time.RFC3339)
			}

			t, err := a.Todos.Create(ctx, todo.CreateParams{
				CalendarID:  calID,
				Summary:     args[0],
				Description: description,
				Location:    location,
				DueDate:     dueDate,
				Priority:    priority,
				Categories:  categories,
				URL:         url,
			})
			if err != nil {
				return fmt.Errorf("create todo: %w", err)
			}

			if len(attachFlags) > 0 {
				attachments, err := parseAttachFlags(attachFlags)
				if err != nil {
					return err
				}
				if err := a.Todos.ReplaceAttachments(ctx, t.ID, attachments); err != nil {
					return fmt.Errorf("add attachments: %w", err)
				}
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, toJSONTodo(t))
			}
			msg := fmt.Sprintf("Created: %s", t.Summary)
			if dueDate != "" {
				msg += fmt.Sprintf(" (due %s)", t.ParseDueDate().Local().Format("Jan 2"))
			}
			fmt.Fprintln(w, msg)
			return nil
		},
	}
	cmd.Flags().StringVar(&dueStr, "due", "", "due date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&calendarName, "calendar", "Personal", "calendar name")
	cmd.Flags().StringVar(&location, "location", "", "location")
	cmd.Flags().StringVar(&description, "description", "", "description")
	cmd.Flags().Int64Var(&priority, "priority", 0, "priority (0-9)")
	cmd.Flags().StringVar(&categories, "categories", "", "comma-separated categories")
	cmd.Flags().StringVar(&url, "url", "", "associated URL")
	cmd.Flags().StringArrayVar(&attachFlags, "attach", nil, "attachment (file path or URL, repeatable)")
	return cmd
}

func todoUpdateCmd() *cobra.Command {
	var (
		summary      string
		dueStr       string
		status       string
		progress     int64
		calendarName string
		location     string
		description  string
		priority     int64
		categories   string
		url          string
		attachFlags  []string
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update an existing todo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid todo ID: %w", err)
			}

			existing, err := a.Todos.Get(ctx, id)
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			p := todo.UpdateParams{
				Summary:         existing.Summary,
				Description:     existing.Description,
				Location:        existing.Location,
				DueDate:         existing.DueDate,
				StartDate:       existing.StartDate,
				Duration:        existing.Duration,
				CompletedAt:     existing.CompletedAt,
				PercentComplete: existing.PercentComplete,
				Status:          existing.Status,
				CalendarID:      existing.CalendarID,
				Priority:        existing.Priority,
				Class:           existing.Class,
				URL:             existing.URL,
				Categories:      existing.Categories,
				RecurrenceRule:  existing.RecurrenceRule,
				Timezone:        existing.Timezone,
				ExDates:         existing.ExDates,
				RDates:          existing.RDates,
			}

			if cmd.Flags().Changed("summary") {
				p.Summary = summary
			}
			if cmd.Flags().Changed("description") {
				p.Description = description
			}
			if cmd.Flags().Changed("location") {
				p.Location = location
			}
			if cmd.Flags().Changed("due") {
				d, err := time.ParseInLocation("2006-01-02", dueStr, time.Local)
				if err != nil {
					return fmt.Errorf("parse due date: %w", err)
				}
				p.DueDate = time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, time.Local).Format(time.RFC3339)
			}
			if cmd.Flags().Changed("status") {
				p.Status = status
				if status == "COMPLETED" {
					p.CompletedAt = time.Now().UTC().Format(time.RFC3339)
					p.PercentComplete = 100
				}
			}
			if cmd.Flags().Changed("progress") {
				p.PercentComplete = progress
			}
			if cmd.Flags().Changed("calendar") {
				calID, err := resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
				p.CalendarID = calID
			}
			if cmd.Flags().Changed("priority") {
				p.Priority = priority
			}
			if cmd.Flags().Changed("categories") {
				p.Categories = categories
			}
			if cmd.Flags().Changed("url") {
				p.URL = url
			}

			t, err := a.Todos.Update(ctx, id, p)
			if err != nil {
				return fmt.Errorf("update todo: %w", err)
			}

			if cmd.Flags().Changed("attach") {
				attachments, err := parseAttachFlags(attachFlags)
				if err != nil {
					return err
				}
				if err := a.Todos.ReplaceAttachments(ctx, t.ID, attachments); err != nil {
					return fmt.Errorf("update attachments: %w", err)
				}
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, toJSONTodo(t))
			}
			printTodo(w, t)
			return nil
		},
	}
	cmd.Flags().StringVar(&summary, "summary", "", "new summary")
	cmd.Flags().StringVar(&dueStr, "due", "", "new due date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&status, "status", "", "new status (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
	cmd.Flags().Int64Var(&progress, "progress", 0, "percent complete (0-100)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "move to calendar")
	cmd.Flags().StringVar(&location, "location", "", "new location")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().Int64Var(&priority, "priority", 0, "new priority (0-9)")
	cmd.Flags().StringVar(&categories, "categories", "", "new categories")
	cmd.Flags().StringVar(&url, "url", "", "new URL")
	cmd.Flags().StringArrayVar(&attachFlags, "attach", nil, "attachment (file path or URL, repeatable)")
	return cmd
}

func todoDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a todo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid todo ID: %w", err)
			}

			if err := a.Todos.Delete(context.Background(), id); err != nil {
				return fmt.Errorf("delete todo: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, map[string]any{"deleted": true, "id": id})
			}
			fmt.Fprintf(w, "Deleted todo %d.\n", id)
			return nil
		},
	}
	return cmd
}

