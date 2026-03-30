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
					RecurrenceID: e.RecurrenceID, Geo: e.Geo,
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
				if len(e.Attachments) > 0 {
					_ = a.Events.ReplaceAttachments(ctx, saved.ID, e.Attachments)
				}
				if len(e.Comments) > 0 {
					_ = a.Events.ReplaceComments(ctx, saved.ID, e.Comments)
				}
				if len(e.Contacts) > 0 {
					_ = a.Events.ReplaceContacts(ctx, saved.ID, e.Contacts)
				}
				if len(e.Resources) > 0 {
					_ = a.Events.ReplaceResources(ctx, saved.ID, e.Resources)
				}
				if len(e.Relations) > 0 {
					_ = a.Events.ReplaceRelations(ctx, saved.ID, e.Relations)
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
					RecurrenceID: t.RecurrenceID, Geo: t.Geo,
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
				if len(t.Attachments) > 0 {
					_ = a.Todos.ReplaceAttachments(ctx, saved.ID, t.Attachments)
				}
				if len(t.Comments) > 0 {
					_ = a.Todos.ReplaceComments(ctx, saved.ID, t.Comments)
				}
				if len(t.Contacts) > 0 {
					_ = a.Todos.ReplaceContacts(ctx, saved.ID, t.Contacts)
				}
				if len(t.Resources) > 0 {
					_ = a.Todos.ReplaceResources(ctx, saved.ID, t.Resources)
				}
				if len(t.Relations) > 0 {
					_ = a.Todos.ReplaceRelations(ctx, saved.ID, t.Relations)
				}
				importedTodos = append(importedTodos, saved)
			}

			if len(result.Warnings) > 0 {
				fmt.Fprintf(os.Stderr, "tcal: %d component(s) skipped during import:\n", len(result.Warnings))
				limit := 5
				if len(result.Warnings) < limit {
					limit = len(result.Warnings)
				}
				for _, w := range result.Warnings[:limit] {
					fmt.Fprintf(os.Stderr, "  - %s\n", w)
				}
				if len(result.Warnings) > 5 {
					fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(result.Warnings)-5)
				}
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				out := map[string]any{
					"events":   toJSONEvents(importedEvents),
					"todos":    toJSONTodos(importedTodos),
					"warnings": result.Warnings,
				}
				return printOutput(w, out)
			}
			fmt.Fprintf(w, "Imported %d events, %d todos.\n", len(importedEvents), len(importedTodos))
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "Personal", "calendar to import into")
	return cmd
}

func icalExportCmd() *cobra.Command {
	var (
		calendarName string
		fromStr      string
		toStr        string
		outFile      string
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
				events[i].Attachments, _ = a.Events.ListAttachments(ctx, events[i].ID)
				events[i].Comments, _ = a.Events.ListComments(ctx, events[i].ID)
				events[i].Contacts, _ = a.Events.ListContacts(ctx, events[i].ID)
				events[i].Resources, _ = a.Events.ListResources(ctx, events[i].ID)
				events[i].Relations, _ = a.Events.ListRelations(ctx, events[i].ID)
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
				todos[i].Attachments, _ = a.Todos.ListAttachments(ctx, todos[i].ID)
				todos[i].Comments, _ = a.Todos.ListComments(ctx, todos[i].ID)
				todos[i].Contacts, _ = a.Todos.ListContacts(ctx, todos[i].ID)
				todos[i].Resources, _ = a.Todos.ListResources(ctx, todos[i].ID)
				todos[i].Relations, _ = a.Todos.ListRelations(ctx, todos[i].ID)
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
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to export.")
				return nil
			}

			if outFile != "" {
				if err := os.WriteFile(outFile, data, 0o644); err != nil {
					return fmt.Errorf("write file: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Exported %d events, %d todos to %s\n", len(events), len(todos), outFile)
			} else {
				cmd.OutOrStdout().Write(data)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "export only this calendar")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 14 days from now)")
	cmd.Flags().StringVarP(&outFile, "file", "f", "", "output file (default: stdout)")
	return cmd
}
