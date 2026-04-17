package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

const icalImportTimeout = 2 * time.Minute

func icalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ical",
		Short: "Import and export iCal (.ics) files",
		Long: `Move data between chroncal and standard iCalendar (.ics) files.

Import accepts VEVENT, VTODO, and VJOURNAL components. Export can emit
any combination of those resource types.`,
		Example: `  chroncal ical import ./calendar.ics
  chroncal ical export --calendar Work --file work.ics`,
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
		Short: "Import events, todos, and journal entries from an iCal (.ics) file",
		Long: `Read an .ics file and upsert its events, todos, and journal entries
into a local calendar.

Entries are matched by UID when possible, so importing the same file
again updates existing items instead of blindly duplicating them.`,
		Example: `  chroncal ical import ./calendar.ics
  chroncal ical import ./team.ics --calendar Work
  chroncal ical import ./dump.ics --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := context.WithTimeout(context.Background(), icalImportTimeout)
			defer cancel()

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
				if _, err := a.Queries.UpsertTimezone(ctx, storage.UpsertTimezoneParams{
					Tzid:          tz.TZID,
					VtimezoneData: tz.Data,
				}); err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("store VTIMEZONE %s: %v", tz.TZID, err))
				}
			}

			// Import events
			var importedEvents []event.Event
			var newEvents, updatedEvents int
			for _, e := range result.Events {
				_, lookupErr := a.Events.GetByUID(ctx, e.UID)
				saved, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
					UID: e.UID, CalendarID: calID,
					Title: e.Title, Description: e.Description, Location: e.Location,
					StartTime: e.StartTime, EndTime: e.EndTime, AllDay: e.AllDay,
					RecurrenceRule: e.RecurrenceRule, Timezone: e.Timezone,
					Status: e.Status, Transp: e.Transp, Sequence: e.Sequence,
					Priority: e.Priority, Class: e.Class, URL: e.URL,
					ConferenceURI: e.ConferenceURI,
					Categories: e.Categories, ExDates: e.ExDates, RDates: e.RDates,
					RecurrenceID: e.RecurrenceID, Geo: e.Geo,
					DurationValue: e.DurationValue, DtStamp: e.DtStamp,
				})
				if err != nil {
					return fmt.Errorf("upsert event %q: %w", safeText(e.Title), err)
				}
				importEventFields(ctx, a.Events, saved.ID, e)
				importedEvents = append(importedEvents, saved)
				if lookupErr != nil {
					newEvents++
				} else {
					updatedEvents++
				}
			}

			// Import todos
			var importedTodos []todo.Todo
			var newTodos, updatedTodos int
			for _, t := range result.Todos {
				_, lookupErr := a.Todos.GetByUID(ctx, t.UID)
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
					return fmt.Errorf("upsert todo %q: %w", safeText(t.Summary), err)
				}
				importTodoFields(ctx, a.Todos, saved.ID, t)
				importedTodos = append(importedTodos, saved)
				if lookupErr != nil {
					newTodos++
				} else {
					updatedTodos++
				}
			}

			// Import journals
			var importedJournals []journal.Journal
			var newJournals, updatedJournals int
			for _, j := range result.Journals {
				_, lookupErr := a.Journals.GetByUID(ctx, j.UID)
				saved, err := a.Journals.UpsertByUID(ctx, journal.UpsertParams{
					UID: j.UID, CalendarID: calID,
					Summary: j.Summary, Description: j.Description,
					StartDate: j.StartDate, Status: j.Status, Class: j.Class,
					URL: j.URL, Categories: j.Categories,
					RecurrenceRule: j.RecurrenceRule, Timezone: j.Timezone,
					Sequence: j.Sequence, ExDates: j.ExDates, RDates: j.RDates,
					RecurrenceID: j.RecurrenceID,
					DtStamp:      j.DtStamp,
				})
				if err != nil {
					return fmt.Errorf("upsert journal %q: %w", safeText(j.Summary), err)
				}
				importJournalFields(ctx, a.Journals, saved.ID, j)
				importedJournals = append(importedJournals, saved)
				if lookupErr != nil {
					newJournals++
				} else {
					updatedJournals++
				}
			}

			if len(result.Warnings) > 0 {
				fmt.Fprintf(os.Stderr, "chroncal: %d component(s) skipped during import:\n", len(result.Warnings))
				limit := 5
				if len(result.Warnings) < limit {
					limit = len(result.Warnings)
				}
				for _, w := range result.Warnings[:limit] {
					fmt.Fprintf(os.Stderr, "  - %s\n", safeText(w))
				}
				if len(result.Warnings) > 5 {
					fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(result.Warnings)-5)
				}
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				out := map[string]any{
					"events":           toJSONEvents(importedEvents),
					"todos":            toJSONTodos(importedTodos),
					"journals":         toJSONJournals(importedJournals),
					"freebusy":         result.FreeBusy,
					"new_events":       newEvents,
					"updated_events":   updatedEvents,
					"new_todos":        newTodos,
					"updated_todos":    updatedTodos,
					"new_journals":     newJournals,
					"updated_journals": updatedJournals,
					"warnings":         result.Warnings,
				}
				return printOutput(w, out)
			}
			fmt.Fprintf(w, "Imported %d new, updated %d existing (%d events, %d todos, %d journals).\n",
				newEvents+newTodos+newJournals, updatedEvents+updatedTodos+updatedJournals,
				len(importedEvents), len(importedTodos), len(importedJournals))
			if len(result.FreeBusy) > 0 {
				fmt.Fprintf(w, "Parsed %d VFREEBUSY component(s); they were not imported into local storage.\n", len(result.FreeBusy))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "calendar to import into (default: first available)")
	return cmd
}

func icalExportCmd() *cobra.Command {
	var (
		calendarName    string
		fromStr         string
		toStr           string
		outFile         string
		category        string
		status          string
		includeEvents   bool
		includeTodos    bool
		includeJournals bool
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export events, todos, and journal entries to iCal (.ics) format",
		Long: `Export local data as iCalendar (.ics).

Without --events, --todos, or --journals, all supported entry types are
included. Use --file to write a file, or omit it to print the .ics data
to stdout.`,
		Example: `  chroncal ical export --calendar Work --file work.ics
  chroncal ical export --events --from 2026-04-01 --to 2026-04-30
  chroncal ical export --todos --category release`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			// Default to including all when no type flags are set
			if !includeEvents && !includeTodos && !includeJournals {
				includeEvents = true
				includeTodos = true
				includeJournals = true
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
					populateEventFields(ctx, a.Events, &events[i])
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
					populateTodoFields(ctx, a.Todos, &todos[i])
				}
			}

			// Load journals
			var journals []journal.Journal
			if includeJournals {
				journals, err = a.Journals.ExportFiltered(ctx, journal.ExportParams{
					CalendarID: calID,
					Category:   category,
					Status:     status,
				})
				if err != nil {
					return fmt.Errorf("list journals: %w", err)
				}
				for i := range journals {
					populateJournalFields(ctx, a.Journals, &journals[i])
				}
			}

			if len(events) == 0 && len(todos) == 0 && len(journals) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to export.")
				return nil
			}

			// Build individual exports and merge them together.
			var parts [][]byte
			if len(events) > 0 {
				eventData, eerr := ical.ExportEvents(events, calendarName)
				if eerr != nil {
					return eerr
				}
				parts = append(parts, eventData)
			}
			if len(todos) > 0 {
				todoData, terr := ical.ExportTodos(todos, calendarName)
				if terr != nil {
					return terr
				}
				parts = append(parts, todoData)
			}
			if len(journals) > 0 {
				journalData, jerr := ical.ExportJournals(journals, calendarName)
				if jerr != nil {
					return jerr
				}
				parts = append(parts, journalData)
			}

			data := parts[0]
			for _, p := range parts[1:] {
				data = ical.MergeCalendars(data, p)
			}

			if outFile != "" {
				if err := os.WriteFile(outFile, data, 0o644); err != nil {
					return fmt.Errorf("write file: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Exported %d events, %d todos, %d journals to %s\n",
					len(events), len(todos), len(journals), outFile)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), string(data))
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
	cmd.Flags().BoolVar(&includeJournals, "journals", false, "include only journal entries")
	return cmd
}
