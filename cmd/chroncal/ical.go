package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
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
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
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
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := context.WithTimeout(context.Background(), icalImportTimeout)
			defer cancel()

			preview, err := icaltransfer.ParseFile(args[0])
			if err != nil {
				return err
			}

			calID, err := resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}
			if err := icaltransfer.ValidateDestination(ctx, a, calID, preview); err != nil {
				return err
			}

			summary := icaltransfer.Import(ctx, a, calID, &preview.Result)
			warnings := summary.Warnings

			if len(warnings) > 0 {
				fmt.Fprintf(os.Stderr, "chroncal: %d warning(s) during import:\n", len(warnings))
				limit := min(5, len(warnings))
				for _, w := range warnings[:limit] {
					fmt.Fprintf(os.Stderr, "  - %s\n", safeText(w))
				}
				if len(warnings) > 5 {
					fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(warnings)-5)
				}
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				out := map[string]any{
					"events":           toJSONEvents(summary.Events),
					"todos":            toJSONTodos(summary.Todos),
					"journals":         toJSONJournals(summary.Journals),
					"freebusy":         preview.Result.FreeBusy,
					"new_events":       summary.NewEvents,
					"updated_events":   summary.UpdatedEvents,
					"new_todos":        summary.NewTodos,
					"updated_todos":    summary.UpdatedTodos,
					"new_journals":     summary.NewJournals,
					"updated_journals": summary.UpdatedJournals,
					"failed":           summary.Failed,
					"warnings":         warnings,
				}
				if err := printOutput(w, out); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(w, "Imported %d new, updated %d existing (%d events, %d todos, %d journals).\n",
					summary.NewEvents+summary.NewTodos+summary.NewJournals,
					summary.UpdatedEvents+summary.UpdatedTodos+summary.UpdatedJournals,
					len(summary.Events), len(summary.Todos), len(summary.Journals))
				if len(preview.Result.FreeBusy) > 0 {
					fmt.Fprintf(w, "Parsed %d VFREEBUSY component(s); they were not imported into local storage.\n", len(preview.Result.FreeBusy))
				}
			}

			// Opportunistically push whatever landed to a CalDAV-linked
			// calendar, mirroring the event/todo/journal write paths, so an
			// import doesn't wait for the next `service run` tick (issue #115).
			// In JSON mode the push seam's human-readable sync note must be
			// discarded so it can't trail the JSON object on stdout and break
			// downstream parsers (issue #255); text mode still shows it.
			if len(summary.Events)+len(summary.Todos)+len(summary.Journals) > 0 {
				pushWriter := w
				if outputFmt != "text" {
					pushWriter = io.Discard
				}
				pushCalendarAfterWrite(a, calID, pushWriter)
			}

			// A non-zero exit signals that the import was partial, but the
			// summary above still reports exactly what landed so the caller
			// can retry only the failed components.
			if summary.Failed > 0 {
				return fmt.Errorf("%d component(s) failed to import; see warnings", summary.Failed)
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

			// Parse date bounds for event filtering. Use parseExportDateBounds
			// (not parseDateRange) so each flag is treated independently:
			// omitting --from leaves the lower bound open and omitting --to
			// leaves the upper bound open. parseDateRange derives defaults
			// (today / from+30d) that are correct for the bounded list view but
			// silently clip the export window when only one flag is given
			// (issue #358).
			fromT, toT, derr := parseExportDateBounds(fromStr, toStr)
			if derr != nil {
				return derr
			}
			// Normalize to UTC so the RFC3339 bounds compare lexically against
			// UTC-stored start/end strings (issue #305). Zero time → empty
			// string → no constraint on that side.
			var fromTime, toTime string
			if !fromT.IsZero() {
				fromTime = fromT.UTC().Format(time.RFC3339)
			}
			if !toT.IsZero() {
				toTime = toT.UTC().Format(time.RFC3339)
			}
			// Todos and journals store DUE / DTSTART as date-only
			// ("YYYY-MM-DD") for all-day items, so use date-only bounds
			// (matching ListTodosFiltered / ListJournalsFiltered) instead of
			// the RFC3339 bounds used for events. A datetime bound would
			// lexically exclude a date-only value on the lower boundary.
			// Format the local wall-clock date the user typed (the bounds were
			// parsed at local midnight); don't convert to UTC, which would shift
			// the date by a day west of UTC and skew the filter.
			var fromDate, toDate string
			if !fromT.IsZero() {
				fromDate = fromT.Format("2006-01-02")
			}
			if !toT.IsZero() {
				toDate = toT.Format("2006-01-02")
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
					extra, eerr := a.Recurrences.ExportExpandedByDateRange(ctx, recurrence.ExportFilterParams{
						CalendarID: calID,
						Category:   category,
						Status:     status,
						From:       fromT,
						To:         toT,
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
					From:       fromDate,
					To:         toDate,
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
					From:       fromDate,
					To:         toDate,
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
