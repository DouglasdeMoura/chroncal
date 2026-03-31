package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/ical"
	"github.com/douglasdemoura/tcal/internal/recurrence"
	"github.com/douglasdemoura/tcal/internal/storage"
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

			// Store imported VTIMEZONE components.
			for _, tz := range result.Timezones {
				_, _ = a.Queries.UpsertTimezone(ctx, storage.UpsertTimezoneParams{
					Tzid:           tz.TZID,
					VtimezoneData:  tz.Data,
				})
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
					DurationValue: e.DurationValue, DtStamp: e.DtStamp,
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
					DtStamp: t.DtStamp,
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
		calendarName  string
		fromStr       string
		toStr         string
		outFile       string
		category      string
		status        string
		includeEvents bool
		includeTodos  bool
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

			// Default to including both when neither flag is set
			if !includeEvents && !includeTodos {
				includeEvents = true
				includeTodos = true
			}

			var calID int64
			if calendarName != "" {
				calID, err = resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
			}

			// Parse date range for event filtering
			var fromTime, toTime string
			if fromStr != "" || toStr != "" {
				from, to, derr := parseDateRange(fromStr, toStr)
				if derr != nil {
					return derr
				}
				fromTime = from.Format(time.RFC3339)
				toTime = to.Format(time.RFC3339)
			}

			// Load events
			var events []event.Event
			if includeEvents {
				events, err = a.Events.ExportFiltered(ctx, event.ExportParams{
					CalendarID: calID,
					From:       fromTime,
					To:         toTime,
					Category:   category,
					Status:     status,
				})
				if err != nil {
					return fmt.Errorf("list events: %w", err)
				}

				// When a date range is specified, recurring masters whose
				// start_time predates the window are missed by the SQL
				// filter.  Include them if any instance falls in range.
				if fromStr != "" || toStr != "" {
					from, to, _ := parseDateRange(fromStr, toStr)
					extra, eerr := a.Recurrences.ExportExpandedByDateRange(ctx, recurrence.ExportFilterParams{
						CalendarID: calID,
						Category:   category,
						Status:     status,
						From:       from,
						To:         to,
					})
					if eerr == nil {
						seen := make(map[int64]bool, len(events))
						for _, e := range events {
							seen[e.ID] = true
						}
						for _, e := range extra {
							if !seen[e.ID] {
								events = append(events, e)
							}
						}
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
			}

			// Load todos
			var todos []todo.Todo
			if includeTodos {
				todos, err = a.Todos.ExportFiltered(ctx, todo.ExportParams{
					CalendarID: calID,
					Category:   category,
					Status:     status,
				})
				if err != nil {
					return fmt.Errorf("list todos: %w", err)
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
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: all)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: all)")
	cmd.Flags().StringVarP(&outFile, "file", "f", "", "output file (default: stdout)")
	cmd.Flags().StringVar(&category, "category", "", "filter by category")
	cmd.Flags().StringVar(&status, "status", "", "filter by status")
	cmd.Flags().BoolVar(&includeEvents, "events", false, "include only events")
	cmd.Flags().BoolVar(&includeTodos, "todos", false, "include only todos")
	return cmd
}
