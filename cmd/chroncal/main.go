package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/maintenance"
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/douglasdemoura/chroncal/internal/tui"
)

// cliError carries a machine-readable code alongside a human message, so
// JSON/YAML output mode can emit `{"error": ..., "code": ...}` for the
// common failure categories. Code is one of: "not_found", "invalid_input",
// "aborted", "error" (default).
type cliError struct {
	Code string
	Msg  string
}

func (e *cliError) Error() string { return e.Msg }

// notFoundErr wraps sql.ErrNoRows into a user-friendly message tagged with
// code "not_found" so machine consumers can dispatch on it.
func notFoundErr(err error, resource string, id any) error {
	if errors.Is(err, sql.ErrNoRows) {
		return &cliError{Code: "not_found", Msg: fmt.Sprintf("%s %v not found", resource, id)}
	}
	return err
}

// errInvalidInputf is the validation-error counterpart to notFoundErr: it
// produces a *cliError tagged with code "invalid_input" so JSON/YAML
// consumers can dispatch on bad-flag / bad-format failures separately
// from genuine internal errors. Use it for date/duration parse failures,
// empty required values, mutually-exclusive flags, and similar.
func errInvalidInputf(format string, args ...any) error {
	return &cliError{Code: "invalid_input", Msg: fmt.Sprintf(format, args...)}
}

// printCLIError writes err to stderr in the format that matches --output.
// Text/table mode keeps "Error: <msg>"; JSON/YAML emit a structured
// payload. Aborted errors drop the "Error: " prefix in text mode — they
// originate from a deliberate refusal, not a system failure.
func printCLIError(err error) {
	code := "error"
	var ce *cliError
	if errors.As(err, &ce) {
		code = ce.Code
	}
	msg := err.Error()

	if outputFmt == "json" || outputFmt == "yaml" {
		payload := map[string]any{"error": msg, "code": code}
		if perr := printOutput(os.Stderr, payload); perr == nil {
			return
		}
		// Fall through to text if the encoder somehow failed.
	}
	if code == "aborted" {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
}

var (
	outputFmt string
	cfg       config.Config
)

// groupRunE is RunE for a parent command with subcommands. Pairing it
// with Args: rejectUnknownSubcommand makes cobra validate args before
// RunE runs, which turns `chroncal alarm tick` (no such subcommand)
// into a clean "unknown command" error with exit 1 instead of silently
// printing help with exit 0.
func groupRunE(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}

// rejectUnknownSubcommand is the Args validator for parent commands.
// Like cobra.NoArgs but tags the error with code "invalid_input" so
// --output json consumers can dispatch on it.
func rejectUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return errInvalidInputf("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return nil
}

// exactArgs is cobra.ExactArgs(n) but re-tags the resulting error as
// "invalid_input" so --output json consumers see a uniform code field
// for arg-count failures instead of the catch-all "error".
func exactArgs(n int) cobra.PositionalArgs {
	inner := cobra.ExactArgs(n)
	return func(cmd *cobra.Command, args []string) error {
		if err := inner(cmd, args); err != nil {
			return &cliError{Code: "invalid_input", Msg: err.Error()}
		}
		return nil
	}
}

// mutuallyExclusive enforces that at most one of the named flags is set,
// returning a *cliError tagged "invalid_input" on conflict. We use this
// instead of cobra.MarkFlagsMutuallyExclusive so the error lands in the
// same taxonomy as every other validation error.
func mutuallyExclusive(cmd *cobra.Command, flags ...string) {
	prev := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		set := make([]string, 0, len(flags))
		for _, name := range flags {
			if c.Flags().Changed(name) {
				set = append(set, "--"+name)
			}
		}
		if len(set) > 1 {
			return errInvalidInputf("%s are mutually exclusive", strings.Join(set, " and "))
		}
		if prev != nil {
			return prev(c, args)
		}
		return nil
	}
}

const (
	groupPlanning    = "planning"
	groupIntegration = "integration"
	groupAutomation  = "automation"
)

var rootCmd = &cobra.Command{
	Use: "chroncal",
	// SilenceUsage stops cobra from dumping the full Examples/Flags block on
	// every RunE error. SilenceErrors hands error printing to main() so we
	// can suppress the duplicate message for errAborted (which already
	// printed its own user-facing line to stderr).
	SilenceUsage:  true,
	SilenceErrors: true,
	// rejectUnknownSubcommand replaces cobra's default legacyArgs so that
	// `chroncal foobar` returns a *cliError tagged "invalid_input" instead
	// of a plain string error — keeping --output json error shape uniform
	// at the root just like it is on every subcommand group.
	Args: rejectUnknownSubcommand,
	Short:         "Terminal calendar with a TUI, scripting, and sync support",
	Long: `chroncal is a local-first terminal calendar backed by SQLite.

Run chroncal with no arguments to open the interactive TUI. Use subcommands
when you want copy-pasteable, scriptable access from the shell or an LLM.

Helpful conventions:
  Dates use YYYY-MM-DD.
  Times use HH:MM in your local timezone unless a command accepts --timezone.
  Machine-friendly output: --output json or --output yaml.
  Event, todo, and journal commands accept either a numeric ID or a UID.
  Recurring overrides can be targeted with --recurrence-id.`,
	Example: `  # Open the interactive terminal UI
  chroncal

  # See the next week of events
  chroncal event list --from 2026-04-01 --to 2026-04-07

  # Create a calendar, then add an event to it
  chroncal calendar create "Work"
  chroncal event add "Team Standup" --calendar Work --date 2026-04-06 --time 09:00 --duration 30m

  # Import an .ics file
  chroncal ical import ./calendar.ics

  # Sync linked CalDAV calendars
  chroncal sync run

  # Get machine-readable output for scripts or LLMs
  chroncal todo list --output json`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cfg = config.Load()
		if cfg.ProductID != "" {
			ical.ProductID = cfg.ProductID
		}
		switch outputFmt {
		case "text", "table", "json", "yaml":
			return nil
		default:
			return errInvalidInputf("invalid output format %q (must be text, table, json, or yaml)", outputFmt)
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := initApp()
		if err != nil {
			return err
		}
		defer a.Close()

		// Kick off the soft-delete purge loop for long-running TUI sessions.
		// PurgeDays=0 (or negative) disables; otherwise runs once up front
		// then every 24h. Detached goroutine — ctx is bound to process
		// lifetime via the signal handler in the TUI loop below.
		purgeDays := cfg.SoftDelete.PurgeDays
		if purgeDays == 0 {
			purgeDays = config.DefaultSoftDeletePurgeDays
		}
		if purgeDays > 0 {
			purger := maintenance.NewPurger(a.Trash, purgeDays, nil)
			go purger.RunDaily(context.Background())
		}

		return tui.Run(a, cfg.UI.Theme)
	},
}

func initApp() (*app.App, error) {
	// Precedence: CHRONCAL_DB env > config.toml > default
	path := cfg.DB
	if path == "" {
		var err error
		path, err = app.DefaultDBPath()
		if err != nil {
			return nil, fmt.Errorf("default db path: %w", err)
		}
	}
	return app.New(path)
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "text", "output format (text, table, json, yaml)")

	rootCmd.AddGroup(
		&cobra.Group{ID: groupPlanning, Title: "Planning and Scheduling"},
		&cobra.Group{ID: groupIntegration, Title: "Import, Sync, and Remote"},
		&cobra.Group{ID: groupAutomation, Title: "Alarms and Background Services"},
	)

	planningCommands := []*cobra.Command{eventCmd(), calendarCmd(), todoCmd(), journalCmd(), freebusyCmd()}
	for _, cmd := range planningCommands {
		cmd.GroupID = groupPlanning
		rootCmd.AddCommand(cmd)
	}

	integrationCommands := []*cobra.Command{icalCmd(), syncCmd()}
	for _, cmd := range integrationCommands {
		cmd.GroupID = groupIntegration
		rootCmd.AddCommand(cmd)
	}

	automationCommands := []*cobra.Command{alarmCmd(), serviceCmd()}
	for _, cmd := range automationCommands {
		cmd.GroupID = groupAutomation
		rootCmd.AddCommand(cmd)
	}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		printCLIError(err)
		os.Exit(1)
	}
}

// resolveEvent looks up an event by numeric ID, string UID, or UID + recurrence-id.
func resolveEvent(ctx context.Context, a *app.App, ref, recurrenceID string) (event.Event, error) {
	var e event.Event
	var err error
	if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		e, err = a.Events.Get(ctx, id)
	} else if recurrenceID != "" {
		e, err = a.Events.GetByUIDAndRecurrenceID(ctx, ref, recurrenceID)
	} else {
		e, err = a.Events.GetByUID(ctx, ref)
	}
	if err != nil {
		return e, notFoundErr(err, "event", ref)
	}
	return e, nil
}

// resolveTodo looks up a todo by numeric ID, string UID, or UID + recurrence-id.
func resolveTodo(ctx context.Context, a *app.App, ref, recurrenceID string) (todo.Todo, error) {
	var t todo.Todo
	var err error
	if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		t, err = a.Todos.Get(ctx, id)
	} else if recurrenceID != "" {
		t, err = a.Todos.GetByUIDAndRecurrenceID(ctx, ref, recurrenceID)
	} else {
		t, err = a.Todos.GetByUID(ctx, ref)
	}
	if err != nil {
		return t, notFoundErr(err, "todo", ref)
	}
	return t, nil
}

func resolveCalendarID(ctx context.Context, a *app.App, name string) (int64, error) {
	cals, err := a.Calendars.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list calendars: %w", err)
	}
	if name == "" {
		// No calendar specified: use the first available calendar.
		if len(cals) == 0 {
			return 0, fmt.Errorf("no calendars exist")
		}
		return cals[0].ID, nil
	}
	for _, c := range cals {
		if strings.EqualFold(c.Name, name) {
			return c.ID, nil
		}
	}
	return 0, fmt.Errorf("calendar %q not found", name)
}

// resolveJournal looks up a journal by numeric ID, string UID, or UID + recurrence-id.
func resolveJournal(ctx context.Context, a *app.App, ref, recurrenceID string) (journal.Journal, error) {
	var j journal.Journal
	var err error
	if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		j, err = a.Journals.Get(ctx, id)
	} else if recurrenceID != "" {
		j, err = a.Journals.GetByUIDAndRecurrenceID(ctx, ref, recurrenceID)
	} else {
		j, err = a.Journals.GetByUID(ctx, ref)
	}
	if err != nil {
		return j, notFoundErr(err, "journal", ref)
	}
	return j, nil
}

func parseDateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 0, 30)

	if fromStr != "" {
		var err error
		from, err = parseCLIDate("from", fromStr, time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if toStr != "" {
		var err error
		to, err = parseCLIDate("to", toStr, time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		to = to.AddDate(0, 0, 1) // half-open: include the entire end day
	}
	return from, to, nil
}

// parseCLIDate parses a YYYY-MM-DD flag value, replacing time.Parse's
// verbose "parsing time ... cannot parse / out of range" surface with a
// clean "--<flag>: invalid date ..." message.
func parseCLIDate(flag, value string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", value, loc)
	if err != nil {
		return time.Time{}, errInvalidInputf("--%s: invalid date %q (expected YYYY-MM-DD)", flag, value)
	}
	return t, nil
}

// parseCLITime parses an HH:MM flag value with the same clean-error
// contract as parseCLIDate.
func parseCLITime(flag, value string) (time.Time, error) {
	t, err := time.Parse("15:04", value)
	if err != nil {
		return time.Time{}, errInvalidInputf("--%s: invalid time %q (expected HH:MM)", flag, value)
	}
	return t, nil
}

// parseCLIDuration parses a Go duration string (e.g. 30m, 1h30m) with
// the same clean-error contract as parseCLIDate.
func parseCLIDuration(flag, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, errInvalidInputf("--%s: invalid duration %q (e.g. 30m, 1h30m)", flag, value)
	}
	return d, nil
}
