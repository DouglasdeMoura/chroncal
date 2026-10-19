package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
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
  chroncal ical import ~/.calendars/work
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
		Use:   "import [path]",
		Short: "Import events, todos, and journal entries from an iCal (.ics) file or directory",
		Long: `Read an .ics file and upsert its events, todos, and journal entries
into a local calendar.

The path can also be a directory. chroncal then reads every .ics file in
that directory and in its subdirectories, and imports them together. A
tool such as vdirsyncer writes one .ics file for each event, so point the
import at the collection directory. chroncal skips every name that starts
with a dot. A file that fails to parse becomes a warning, and the rest of
the directory still lands.

Without a path, the command imports every calendar that has a directory
(issue #809). Set the directory with "calendar create --path", "calendar
update --path", or a [[calendars]] entry in config.toml. With --calendar,
the command imports only that calendar from its directory.

Entries are matched by UID when possible, so importing the same path
again updates existing items instead of blindly duplicating them.`,
		Example: `  chroncal ical import ./calendar.ics
  chroncal ical import ~/.calendars/work --calendar Work
  chroncal ical import ~/.calendars --calendar Personal
  chroncal ical import ./dump.ics --output json
  chroncal ical import                      # every calendar with a directory
  chroncal ical import --calendar Fastmail  # one calendar from its directory`,
		Args: atMostOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := context.WithTimeout(context.Background(), icalImportTimeout)
			defer cancel()

			targets, err := icalImportTargets(ctx, a, args, calendarName)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			var (
				outcomes []icalImportOutcome
				failed   int
			)
			for _, target := range targets {
				outcome, err := importIntoCalendar(ctx, a, target)
				if err != nil {
					if len(targets) == 1 {
						return err
					}
					// A batch run reports the calendar that failed and
					// continues, so one missing directory does not stop
					// the other calendars.
					fmt.Fprintf(os.Stderr, "chroncal: %s: %v\n", safeText(target.calendarName), err)
					failed++
					continue
				}
				outcomes = append(outcomes, outcome)
				printImportWarnings(outcome.summary.Warnings)
				if outputFmt == "text" {
					printImportSummary(w, outcome, len(targets) > 1)
				}
				// Opportunistically push whatever landed to a CalDAV-linked
				// calendar, mirroring the event/todo/journal write paths, so an
				// import doesn't wait for the next ` + "`service run`" + ` tick (issue #115).
				if outcome.landed() > 0 {
					opportunisticPush(a, target.calendarID, cmd)
				}
				failed += outcome.summary.Failed
			}

			if outputFmt != "text" {
				if len(targets) == 1 && len(outcomes) == 1 {
					if err := printOutput(w, outcomes[0].json()); err != nil {
						return err
					}
				} else {
					items := make([]map[string]any, 0, len(outcomes))
					for _, outcome := range outcomes {
						items = append(items, outcome.json())
					}
					if err := printOutput(w, items); err != nil {
						return err
					}
				}
			}

			// A non-zero exit signals that the import was partial, but the
			// summary above still reports exactly what landed so the caller
			// can retry only the failed components.
			if failed > 0 {
				return fmt.Errorf("%d component(s) or calendar(s) failed to import; see warnings", failed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "calendar to import into (default: first available)")
	return cmd
}

// icalImportTarget is one import run: a source path and the calendar that
// receives it.
type icalImportTarget struct {
	path         string
	calendarID   int64
	calendarName string
}

// icalImportTargets resolves the import runs of one command. With a path
// argument there is one run into the --calendar (or the default calendar).
// Without a path, each calendar that has a directory is one run (issue
// #809). With --calendar and no path, that calendar must have a directory.
func icalImportTargets(ctx context.Context, a *app.App, args []string, calendarName string) ([]icalImportTarget, error) {
	if len(args) == 1 {
		calID, err := resolveCalendarID(ctx, a, calendarName)
		if err != nil {
			return nil, err
		}
		cal, err := a.Calendars.Get(ctx, calID)
		if err != nil {
			return nil, fmt.Errorf("get calendar: %w", err)
		}
		return []icalImportTarget{{path: args[0], calendarID: calID, calendarName: cal.Name}}, nil
	}
	cals, err := a.Calendars.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	if calendarName != "" {
		cal, err := findCalendarByRef(cals, calendarName)
		if err != nil {
			return nil, err
		}
		if cal.LocalPath == "" {
			return nil, errInvalidInputf("calendar %q has no directory; pass a path, or set one with 'chroncal calendar update %q --path DIR'", cal.Name, cal.Name)
		}
		return []icalImportTarget{{path: cal.LocalPath, calendarID: cal.ID, calendarName: cal.Name}}, nil
	}
	var targets []icalImportTarget
	for _, cal := range cals {
		if cal.LocalPath == "" {
			continue
		}
		targets = append(targets, icalImportTarget{path: cal.LocalPath, calendarID: cal.ID, calendarName: cal.Name})
	}
	if len(targets) == 0 {
		return nil, errInvalidInputf("no calendar has a directory; pass a path, or set one with 'chroncal calendar update NAME --path DIR'")
	}
	return targets, nil
}

// icalImportOutcome is the result of one import run.
type icalImportOutcome struct {
	target  icalImportTarget
	summary icaltransfer.Summary
	// freeBusy counts the VFREEBUSY components that the parse found. The
	// import does not store them.
	freeBusy int
}

func (o icalImportOutcome) landed() int {
	return len(o.summary.Events) + len(o.summary.Todos) + len(o.summary.Journals)
}

func (o icalImportOutcome) json() map[string]any {
	return map[string]any{
		"calendar_id":      o.target.calendarID,
		"calendar":         o.target.calendarName,
		"path":             o.target.path,
		"events":           toJSONEvents(o.summary.Events),
		"todos":            toJSONTodos(o.summary.Todos),
		"journals":         toJSONJournals(o.summary.Journals),
		"freebusy":         o.freeBusy,
		"new_events":       o.summary.NewEvents,
		"updated_events":   o.summary.UpdatedEvents,
		"new_todos":        o.summary.NewTodos,
		"updated_todos":    o.summary.UpdatedTodos,
		"new_journals":     o.summary.NewJournals,
		"updated_journals": o.summary.UpdatedJournals,
		"failed":           o.summary.Failed,
		"warnings":         o.summary.Warnings,
	}
}

// importIntoCalendar parses the source of one target and imports it into
// the calendar of that target.
func importIntoCalendar(ctx context.Context, a *app.App, target icalImportTarget) (icalImportOutcome, error) {
	preview, err := icaltransfer.ParsePath(target.path)
	if err != nil {
		return icalImportOutcome{}, err
	}
	if err := icaltransfer.ValidateDestination(ctx, a, target.calendarID, preview); err != nil {
		return icalImportOutcome{}, err
	}
	summary := icaltransfer.Import(ctx, a, target.calendarID, &preview.Result)
	return icalImportOutcome{
		target:   target,
		summary:  summary,
		freeBusy: len(preview.Result.FreeBusy),
	}, nil
}

func printImportWarnings(warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "chroncal: %d warning(s) during import:\n", len(warnings))
	limit := min(5, len(warnings))
	for _, w := range warnings[:limit] {
		fmt.Fprintf(os.Stderr, "  - %s\n", safeText(w))
	}
	if len(warnings) > 5 {
		fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(warnings)-5)
	}
}

// printImportSummary prints the text summary of one run. A batch run
// names the calendar first, so the user can tell the runs apart.
func printImportSummary(w io.Writer, o icalImportOutcome, named bool) {
	if named {
		fmt.Fprintf(w, "%s (%s): ", safeText(o.target.calendarName), o.target.path)
	}
	fmt.Fprintf(w, "Imported %d new, updated %d existing (%d events, %d todos, %d journals).\n",
		o.summary.NewEvents+o.summary.NewTodos+o.summary.NewJournals,
		o.summary.UpdatedEvents+o.summary.UpdatedTodos+o.summary.UpdatedJournals,
		len(o.summary.Events), len(o.summary.Todos), len(o.summary.Journals))
	if o.freeBusy > 0 {
		fmt.Fprintf(w, "Parsed %d VFREEBUSY component(s); they were not imported into local storage.\n", o.freeBusy)
	}
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
		skipUnreadable  bool
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export events, todos, and journal entries to iCal (.ics) format",
		Long: `Export local data as iCalendar (.ics).

Without --events, --todos, or --journals, all supported entry types are
included. Use --file to write a file, or omit it to print the .ics data
to stdout.

A failed relation read aborts the export by default. No file is then
written, so no incomplete backup exists. Pass --skip-unreadable to
continue past records with unreadable relations. The file then carries
a comment header that names each incomplete record, and stderr lists
them.`,
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

			// Records that exported without all their relations. The default
			// path aborts instead, so this stays empty there.
			var incomplete []unreadableRecord

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
					events, err = appendRecurringExtras(events, extra, eerr)
					if err != nil {
						return err
					}
				}

				for i := range events {
					// Export writes a file the user will treat as authoritative, so a
					// failed relation read must abort rather than quietly produce an
					// .ics missing alarms and attendees. Name the offending record
					// and say no file was written, so the message is actionable
					// rather than a bare "event 4211 attachments: ...".
					//
					// With --skip-unreadable the user chose that trade-off for this
					// run: keep every readable relation, and mark what went missing.
					if err := a.Events.Hydrate(ctx, &events[i]); err != nil {
						if !skipUnreadable {
							return fmt.Errorf("export aborted, no file written: %w (event uid %s)", err, events[i].UID)
						}
						if failed := a.Events.HydrateSkipUnreadable(ctx, &events[i]); len(failed) > 0 {
							incomplete = append(incomplete, unreadableRecord{"event", events[i].ID, events[i].UID, failed})
						}
					}
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
					if err := a.Todos.Hydrate(ctx, &todos[i]); err != nil {
						if !skipUnreadable {
							return fmt.Errorf("export aborted, no file written: %w (todo uid %s)", err, todos[i].UID)
						}
						if failed := a.Todos.HydrateSkipUnreadable(ctx, &todos[i]); len(failed) > 0 {
							incomplete = append(incomplete, unreadableRecord{"todo", todos[i].ID, todos[i].UID, failed})
						}
					}
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
					if err := a.Journals.Hydrate(ctx, &journals[i]); err != nil {
						if !skipUnreadable {
							return fmt.Errorf("export aborted, no file written: %w (journal uid %s)", err, journals[i].UID)
						}
						if failed := a.Journals.HydrateSkipUnreadable(ctx, &journals[i]); len(failed) > 0 {
							incomplete = append(incomplete, unreadableRecord{"journal", journals[i].ID, journals[i].UID, failed})
						}
					}
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

			// The file must self-describe months later without terminal
			// context, so the caveat goes into the file as comment lines and
			// not only onto this run's stderr.
			if len(incomplete) > 0 {
				reportUnreadable(incomplete)
				data = ical.PrependComments(data, unreadableCaveatLines(incomplete))
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
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD or relative, default: all)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD or relative, default: all)")
	cmd.Flags().StringVarP(&outFile, "file", "f", "", "output file (default: stdout)")
	cmd.Flags().StringVar(&category, "category", "", "filter by category")
	cmd.Flags().StringVar(&status, "status", "", "filter by status")
	cmd.Flags().BoolVar(&includeEvents, "events", false, "include only events")
	cmd.Flags().BoolVar(&includeTodos, "todos", false, "include only todos")
	cmd.Flags().BoolVar(&includeJournals, "journals", false, "include only journal entries")
	cmd.Flags().BoolVar(&skipUnreadable, "skip-unreadable", false,
		"export past unreadable relations and mark incomplete records (default: abort)")
	return cmd
}

// appendRecurringExtras merges recurring masters from the expansion query
// into the export set. expandErr is the error from that query. A failed
// expansion must abort the export: a backup file that silently drops
// series masters is incomplete, so no file may be written.
func appendRecurringExtras(events, extra []event.Event, expandErr error) ([]event.Event, error) {
	if expandErr != nil {
		return nil, fmt.Errorf("export aborted, no file written: expand recurring events: %w", expandErr)
	}
	seen := make(map[int64]bool, len(events))
	for _, e := range events {
		seen[e.ID] = true
	}
	for _, e := range extra {
		if !seen[e.ID] {
			events = append(events, e)
		}
	}
	return events, nil
}

// unreadableRecord names one record that exported without all its relations.
type unreadableRecord struct {
	kind      string
	id        int64
	uid       string
	relations []string
}

// reportUnreadable prints one line per incomplete record to stderr. The line
// carries the row id, the uid, and the names of the lost relations.
func reportUnreadable(records []unreadableRecord) {
	fmt.Fprintf(os.Stderr, "chroncal: --skip-unreadable: %d record(s) are incomplete:\n", len(records))
	for _, r := range records {
		fmt.Fprintf(os.Stderr, "  %s %d (uid %s): %s\n", r.kind, r.id, r.uid, strings.Join(r.relations, ", "))
	}
}

// unreadableCaveatLines builds the comment lines that PrependComments writes
// into the .ics. A reader months later then sees which records are incomplete
// and what each one misses, with no terminal output to rely on.
func unreadableCaveatLines(records []unreadableRecord) []string {
	lines := make([]string, 0, 2+len(records))
	lines = append(lines,
		"chroncal wrote this file with --skip-unreadable.",
		"The records below are incomplete. Their named relations are absent:")
	for _, r := range records {
		lines = append(lines, fmt.Sprintf("%s %d (uid %s): %s",
			r.kind, r.id, r.uid, strings.Join(r.relations, ", ")))
	}
	return lines
}
