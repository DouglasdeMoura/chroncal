package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/ical"
	"github.com/douglasdemoura/tcal/internal/todo"
)

func icalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ical",
		Short: "Import and export iCal (.ics) files",
	}
	cmd.AddCommand(icalImportCmd(), icalExportCmd())
	return cmd
}

func icalImportCmd() *cobra.Command {
	var (
		calendarName string
		jsonOut      bool
	)
	cmd := &cobra.Command{
		Use:   "import <file.ics>",
		Short: "Import events and todos from an iCal (.ics) file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open file: %w", err)
			}
			defer f.Close()

			result, err := ical.ImportFile(f)
			if err != nil {
				return fmt.Errorf("import: %w", err)
			}

			calID, err := resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}

			// Import events
			var importedEvents []event.Event
			for _, e := range result.Events {
				saved, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
					UID: e.UID, CalendarID: calID,
					Title: e.Title, Description: e.Description, Location: e.Location,
					StartTime: e.StartTime, EndTime: e.EndTime, AllDay: e.AllDay,
					RecurrenceRule: e.RecurrenceRule, Timezone: e.Timezone,
					Status: e.Status, Transp: e.Transp, Sequence: e.Sequence,
					Priority: e.Priority, Class: e.Class, URL: e.URL,
					Categories: e.Categories, ExDates: e.ExDates, RDates: e.RDates,
					RecurrenceID: e.RecurrenceID,
				})
				if err != nil {
					return fmt.Errorf("upsert event %q: %w", e.Title, err)
				}
				if len(e.Alarms) > 0 {
					_ = a.Events.ReplaceAlarms(ctx, saved.ID, e.Alarms)
				}
				if len(e.Attendees) > 0 {
					_ = a.Events.ReplaceAttendees(ctx, saved.ID, e.Attendees)
				}
				importedEvents = append(importedEvents, saved)
			}

			// Import todos
			var importedTodos []todo.Todo
			for _, t := range result.Todos {
				saved, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{
					UID: t.UID, CalendarID: calID,
					Summary: t.Summary, Description: t.Description, Location: t.Location,
					DueDate: t.DueDate, StartDate: t.StartDate, Duration: t.Duration,
					CompletedAt: t.CompletedAt, PercentComplete: t.PercentComplete,
					Status: t.Status, Priority: t.Priority, Class: t.Class,
					URL: t.URL, Categories: t.Categories,
					RecurrenceRule: t.RecurrenceRule, Timezone: t.Timezone,
					Sequence: t.Sequence, ExDates: t.ExDates, RDates: t.RDates,
					RecurrenceID: t.RecurrenceID,
				})
				if err != nil {
					return fmt.Errorf("upsert todo %q: %w", t.Summary, err)
				}
				if len(t.Alarms) > 0 {
					_ = a.Todos.ReplaceAlarms(ctx, saved.ID, t.Alarms)
				}
				if len(t.Attendees) > 0 {
					_ = a.Todos.ReplaceAttendees(ctx, saved.ID, t.Attendees)
				}
				importedTodos = append(importedTodos, saved)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				out := map[string]any{
					"events": toJSONEvents(importedEvents),
					"todos":  toJSONTodos(importedTodos),
				}
				return printJSON(w, out)
			}
			fmt.Fprintf(w, "tcal: imported %d events, %d todos\n", len(importedEvents), len(importedTodos))
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "Personal", "calendar to import into")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output imported items as JSON")
	return cmd
}

func icalExportCmd() *cobra.Command {
	var (
		calendarName string
		fromStr      string
		toStr        string
		output       string
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export events and todos to iCal (.ics) format",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			from, to, err := parseDateRange(fromStr, toStr)
			if err != nil {
				return err
			}

			// Load events
			var events []event.Event
			if calendarName != "" {
				calID, err := resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
				events, err = a.Events.ListByCalendarAndDateRange(ctx, calID, from, to)
				if err != nil {
					return fmt.Errorf("list events: %w", err)
				}
			} else {
				events, err = a.Events.ListByDateRange(ctx, from, to)
				if err != nil {
					return fmt.Errorf("list events: %w", err)
				}
			}
			for i := range events {
				events[i].Alarms, _ = a.Events.ListAlarms(ctx, events[i].ID)
				events[i].Attendees, _ = a.Events.ListAttendees(ctx, events[i].ID)
			}

			// Load todos
			var todos []todo.Todo
			if calendarName != "" {
				calID, _ := resolveCalendarID(ctx, a, calendarName)
				todos, _ = a.Todos.ListByCalendar(ctx, calID)
			} else {
				todos, _ = a.Todos.ListAll(ctx)
			}
			for i := range todos {
				todos[i].Alarms, _ = a.Todos.ListAlarms(ctx, todos[i].ID)
				todos[i].Attendees, _ = a.Todos.ListAttendees(ctx, todos[i].ID)
			}

			var data []byte
			switch {
			case len(events) > 0 && len(todos) > 0:
				eventData, err := ical.ExportEvents(events, calendarName)
				if err != nil {
					return err
				}
				todoData, err := ical.ExportTodos(todos, calendarName)
				if err != nil {
					return err
				}
				data = ical.MergeCalendars(eventData, todoData)
			case len(events) > 0:
				data, err = ical.ExportEvents(events, calendarName)
				if err != nil {
					return err
				}
			case len(todos) > 0:
				data, err = ical.ExportTodos(todos, calendarName)
				if err != nil {
					return err
				}
			default:
				fmt.Fprintln(cmd.OutOrStdout(), "tcal: nothing to export")
				return nil
			}

			if output != "" {
				if err := os.WriteFile(output, data, 0o644); err != nil {
					return fmt.Errorf("write file: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "tcal: exported %d events, %d todos to %s\n", len(events), len(todos), output)
			} else {
				cmd.OutOrStdout().Write(data)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "export only this calendar")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 14 days from now)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file (default: stdout)")
	return cmd
}
