package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func calendarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "calendar",
		Aliases: []string{"cal"},
		Short:   "Manage calendars",
	}
	cmd.AddCommand(calendarListCmd(), calendarGetCmd(), calendarCreateCmd(), calendarUpdateCmd(), calendarDeleteCmd())
	return cmd
}

func calendarListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all calendars",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			cals, err := a.Calendars.List(context.Background())
			if err != nil {
				return fmt.Errorf("list calendars: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				items := make([]jsonCalendar, len(cals))
				for i, c := range cals {
					items[i] = toJSONCalendar(c)
				}
				return printOutput(w, items)
			}
			printCalendars(w, cals)
			return nil
		},
	}
	return cmd
}

func calendarGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get calendar details by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid calendar ID: %w", err)
			}

			c, err := a.Calendars.Get(context.Background(), id)
			if err != nil {
				return notFoundErr(err, "calendar", id)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONCalendar(c))
			}
			printCalendar(w, c)
			return nil
		},
	}
	return cmd
}

func calendarCreateCmd() *cobra.Command {
	var (
		color       string
		description string
	)
	cmd := &cobra.Command{
		Use:   `create "<name>"`,
		Short: "Create a new calendar",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("calendar name must not be empty")
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			c, err := a.Calendars.Create(context.Background(), args[0], color, description)
			if err != nil {
				return fmt.Errorf("create calendar: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONCalendar(c))
			}
			fmt.Fprintf(w, "Created calendar %d: %s\n", c.ID, c.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&color, "color", "#7C3AED", "calendar color (hex)")
	cmd.Flags().StringVar(&description, "description", "", "calendar description")
	return cmd
}

func calendarUpdateCmd() *cobra.Command {
	var (
		name        string
		color       string
		description string
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update an existing calendar",
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
				return fmt.Errorf("invalid calendar ID: %w", err)
			}

			existing, err := a.Calendars.Get(ctx, id)
			if err != nil {
				return notFoundErr(err, "calendar", id)
			}

			n, co, d := existing.Name, existing.Color, existing.Description
			if cmd.Flags().Changed("name") {
				n = name
			}
			if cmd.Flags().Changed("color") {
				co = color
			}
			if cmd.Flags().Changed("description") {
				d = description
			}

			c, err := a.Calendars.Update(ctx, id, n, co, d)
			if err != nil {
				return fmt.Errorf("update calendar: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONCalendar(c))
			}
			fmt.Fprintf(w, "Updated calendar %d: %s\n", c.ID, c.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&color, "color", "", "new color (hex)")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	return cmd
}

func calendarDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a calendar",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid calendar ID: %w", err)
			}

			if err := a.Calendars.Delete(context.Background(), id); err != nil {
				return notFoundErr(err, "calendar", id)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"deleted": true, "id": id})
			}
			fmt.Fprintf(w, "Deleted calendar %d.\n", id)
			return nil
		},
	}
	return cmd
}
