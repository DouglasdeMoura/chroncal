// Command chroncal is a terminal calendar, todo, and journal manager.
// It supports RFC 5545 (iCalendar) and CalDAV sync. It offers a CLI for
// scripts and an interactive TUI. SQLite stores the data locally.
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

// cliError carries a machine-readable code alongside a human message. JSON
// and YAML output can emit `{"error": ..., "code": ...}` for the common
// failure categories. Code is one of: "not_found", "invalid_input",
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

// errInvalidInputf is the validation-error counterpart to notFoundErr. It
// produces a *cliError tagged with code "invalid_input". JSON and YAML
// consumers can then dispatch on bad-flag or bad-format failures separately
// from genuine internal errors. Use it for date or duration parse failures,
// empty required values, mutually exclusive flags, and similar cases.
func errInvalidInputf(format string, args ...any) error {
	return &cliError{Code: "invalid_input", Msg: fmt.Sprintf(format, args...)}
}

// printCLIError writes err to stderr in the format that matches --output.
// Text mode keeps "Error: <msg>". JSON emits a structured payload.
// Aborted errors drop the "Error: " prefix in text mode. They come from
// a deliberate refusal, not a system failure.
//
// When the chain contains a *cliError we surface its Msg directly. The
// function strips fmt.Errorf wrap prefixes that would leak internal call
// sites (for example "get event: event 999 not found") into the user-facing
// message.
func printCLIError(err error) {
	code := "error"
	msg := err.Error()
	var ce *cliError
	if errors.As(err, &ce) {
		code = ce.Code
		msg = ce.Msg
	}

	if outputFmt == "json" {
		payload := map[string]any{"error": msg, "code": code}
		if perr := printOutput(os.Stderr, payload); perr == nil {
			return
		}
		// Fall through to text if the encoder failed.
	}
	if code == "aborted" {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
}

var (
	outputFmt       string
	allowPlaintext  bool
	tuiEventRef     string
	tuiRecurrenceID string
	tuiAt           string
	cfg             config.Config
)

// groupRunE is RunE for a parent command with subcommands. Pair it with
// Args: rejectUnknownSubcommand so cobra validates args before RunE runs.
// Then `chroncal alarm tick` (no such subcommand) becomes a clean
// "unknown command" error with exit 1. Cobra does not print help with exit 0.
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

// exactOneArg is cobra.ExactArgs(1) but re-tags the error as "invalid_input".
// Then --output json consumers see a uniform code field for arg-count
// failures instead of the catch-all "error". Every command with positional
// args takes exactly one today. Generalize if that changes.
func exactOneArg(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return &cliError{Code: "invalid_input", Msg: err.Error()}
	}
	return nil
}

// mutuallyExclusive enforces that at most one of the named flags is set.
// On conflict it returns a *cliError tagged "invalid_input". We use this
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
	// SilenceUsage stops cobra from emitting the full Examples/Flags block on
	// every RunE error. SilenceErrors hands error print to main() so we can
	// suppress the duplicate message for errAborted. That error already
	// printed its own user-facing line to stderr.
	SilenceUsage:  true,
	SilenceErrors: true,
	// rejectUnknownSubcommand replaces cobra's default legacyArgs so that
	// `chroncal foobar` returns a *cliError tagged "invalid_input" instead
	// of a plain string error. The --output json error shape then stays
	// uniform at the root, as it is on every subcommand group.
	Args:  rejectUnknownSubcommand,
	Short: "Terminal calendar with a TUI, scripting, and sync support",
	Long: `chroncal is a local-first terminal calendar backed by SQLite.

Run chroncal with no arguments to open the interactive TUI. Use subcommands
when you want copy-pasteable, scriptable access from the shell or an LLM.

Helpful conventions:
  Dates use YYYY-MM-DD.
  Times use HH:MM in your local timezone unless a command accepts --timezone.
  Text output renders timestamps in your local timezone; --output json
  emits RFC 3339 UTC (e.g. 2026-04-01T09:00:00Z) so scripts can compare
  them without dealing with offsets.
  Machine-friendly output: --output json.
  Event, todo, and journal commands accept either a numeric ID or a UID.
  Recurring overrides can be targeted with --recurrence-id.`,
	Example: `  # Open the interactive terminal UI
  chroncal

  # Open the TUI on a specific event
  chroncal --event 42
  chroncal --event 42 --at 2026-04-17T14:00:00Z

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
		var err error
		cfg, err = config.Load()
		if err != nil {
			return err
		}
		if cfg.ProductID != "" {
			ical.ProductID = cfg.ProductID
		}
		switch outputFmt {
		case "text", "json":
			return nil
		default:
			return errInvalidInputf("invalid output format %q (must be text or json)", outputFmt)
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := initApp()
		if err != nil {
			return err
		}
		defer a.Close()

		// Start the soft-delete purge loop for long-running TUI sessions.
		// config.Load already resolved the default. PurgeDays=0 (or negative)
		// means the user disabled purge, so leave it off. Otherwise run once
		// up front, then every 24h. Detached goroutine. ctx is bound to process
		// lifetime via the signal handler in the TUI loop below.
		if purgeDays := cfg.SoftDelete.PurgeDays; purgeDays > 0 {
			purger := maintenance.NewPurger(a.Trash, a.Queries, purgeDays, config.SharedStateDirLogger())
			go purger.RunDaily(context.Background())
		}

		var openEvent event.Event
		if tuiEventRef != "" || tuiRecurrenceID != "" || tuiAt != "" {
			var openErr error
			openEvent, openErr = resolveTUIOpenEvent(context.Background(), a, tuiEventRef, tuiRecurrenceID, tuiAt)
			if openErr != nil {
				return openErr
			}
		}

		weekStart := time.Sunday
		if w, ok := config.ParseWeekStart(cfg.UI.WeekStart); ok {
			weekStart = w
		}
		return tui.Run(a, cfg.UI.Theme, tui.RunOptions{Event: openEvent, WeekStart: weekStart, SyncConflictStrategy: cfg.Sync.ConflictStrategy})
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
	a, err := app.New(path)
	if err != nil {
		return nil, err
	}
	// Permit the plaintext credential-store fallback only on explicit
	// opt-in, via config or the --allow-plaintext flag. Either one suffices.
	a.AllowPlaintext = cfg.Security.AllowPlaintext || allowPlaintext
	return a, nil
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "text", "output format (text, json)")
	rootCmd.PersistentFlags().BoolVar(&allowPlaintext, "allow-plaintext", false, "permit storing credentials in plaintext when no OS keyring is available")
	rootCmd.Flags().StringVar(&tuiEventRef, "event", "", "open the TUI on this event (ID or UID)")
	rootCmd.Flags().StringVar(&tuiRecurrenceID, "recurrence-id", "", "open a recurrence override (use with a series UID)")
	rootCmd.Flags().StringVar(&tuiAt, "at", "", "open a generated occurrence at this time (RFC 3339 or YYYY-MM-DD)")

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

	integrationCommands := []*cobra.Command{accountCmd(), icalCmd(), syncCmd()}
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

// resolveRef looks up a resource by numeric ID, string UID, or UID +
// recurrence-id, using the three lookup functions for the resource kind. It
// is the shared body behind resolveEvent / resolveTodo / resolveJournal.
func resolveRef[T any](
	ctx context.Context,
	ref, recurrenceID, kind string,
	getByID func(context.Context, int64) (T, error),
	getByUID func(context.Context, string) (T, error),
	getByUIDAndRecurrenceID func(context.Context, string, string) (T, error),
) (T, error) {
	var v T
	var err error
	if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		// A numeric ref addresses a single row by its unique ID. A
		// recurrence-id can only narrow a UID to one instance. A pair of
		// recurrence-id and ID is contradictory. If the command honors the ID,
		// it drops the recurrence-id. For delete or update, that acts on the
		// master while the prompt claims to touch one occurrence. Reject.
		// Do not guess. To target an override by recurrence-id, pass the series UID. See #114.
		if recurrenceID != "" {
			return v, errInvalidInputf("--recurrence-id cannot be combined with a numeric %s ID %q; use the series UID to address an override instance", kind, ref)
		}
		v, err = getByID(ctx, id)
	} else if recurrenceID != "" {
		v, err = getByUIDAndRecurrenceID(ctx, ref, recurrenceID)
	} else {
		v, err = getByUID(ctx, ref)
	}
	if err != nil {
		return v, notFoundErr(err, kind, ref)
	}
	return v, nil
}

// resolveEvent looks up an event by numeric ID, string UID, or UID + recurrence-id.
func resolveEvent(ctx context.Context, a *app.App, ref, recurrenceID string) (event.Event, error) {
	return resolveRef(ctx, ref, recurrenceID, "event",
		a.Events.Get, a.Events.GetByUID, a.Events.GetByUIDAndRecurrenceID)
}

// resolveTodo looks up a todo by numeric ID, string UID, or UID + recurrence-id.
func resolveTodo(ctx context.Context, a *app.App, ref, recurrenceID string) (todo.Todo, error) {
	return resolveRef(ctx, ref, recurrenceID, "todo",
		a.Todos.Get, a.Todos.GetByUID, a.Todos.GetByUIDAndRecurrenceID)
}

func resolveCalendarID(ctx context.Context, a *app.App, name string) (int64, error) {
	if name == "" {
		// No calendar specified: use the database default. Fall back to
		// the first calendar if the default row was deleted out of band.
		// Never pick "no calendar" in silence. Every write needs a parent
		// calendar.
		def, err := a.Calendars.GetDefault(ctx)
		if err == nil {
			return def.ID, nil
		}
		cals, listErr := a.Calendars.List(ctx)
		if listErr != nil {
			return 0, fmt.Errorf("list calendars: %w", listErr)
		}
		if len(cals) == 0 {
			return 0, fmt.Errorf("no calendars exist")
		}
		return cals[0].ID, nil
	}
	cals, err := a.Calendars.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list calendars: %w", err)
	}
	cal, err := findCalendarByRef(cals, name)
	if err != nil {
		return 0, err
	}
	return cal.ID, nil
}

// resolveJournal looks up a journal by numeric ID, string UID, or UID + recurrence-id.
func resolveJournal(ctx context.Context, a *app.App, ref, recurrenceID string) (journal.Journal, error) {
	return resolveRef(ctx, ref, recurrenceID, "journal",
		a.Journals.Get, a.Journals.GetByUID, a.Journals.GetByUIDAndRecurrenceID)
}

func parseDateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)

	if fromStr != "" {
		var err error
		from, err = parseCLIDate("from", fromStr, time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	// Default the window end to 30 days after `from` (not after today). Then a
	// `--from` far in the future without `--to` still yields a forward,
	// non-empty range instead of an inverted one (issue #111).
	to := from.AddDate(0, 0, 30)
	if toStr != "" {
		var err error
		to, err = parseCLIDate("to", toStr, time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		to = to.AddDate(0, 0, 1) // half-open: include the entire end day
	}
	// An inverted window (--to before --from) silently matched nothing.
	// Reject it so a typo gives an error instead of an empty result.
	if !to.After(from) {
		return time.Time{}, time.Time{}, errInvalidInputf("--to must be after --from")
	}
	return from, to, nil
}

// parseExportDateBounds parses the optional --from/--to flags for export
// commands. It treats each bound independently. An omitted bound returns a
// zero time.Time (open/unbounded). Then only --from or only --to is honoured
// without a silent derived end from today or from+30 days.
//
// This corrects issue #358. The export command shared parseDateRange, which
// is for the "today..+30d" list default. That path clipped the window when
// exactly one flag was given.
func parseExportDateBounds(fromStr, toStr string) (time.Time, time.Time, error) {
	var from, to time.Time
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
	// Both bounds set: an inverted window silently matched nothing.
	// One bound alone stays open, so the check only runs when both exist.
	if !from.IsZero() && !to.IsZero() && !to.After(from) {
		return time.Time{}, time.Time{}, errInvalidInputf("--to must be after --from")
	}
	return from, to, nil
}

// parseListDateRange parses the optional --from/--to window for the
// retrospective `todo list` / `journal list` views. With no flags it returns
// an open (zero) range. Overdue todos and past journal entries then stay
// visible by default (issue #304). Non-recurring rows are listed unfiltered.
// Recurring masters are returned as-is.
//
// When either flag is set it delegates to parseDateRange. That keeps the
// finite forward window those explicit queries expect. Recurrence expansion
// needs that window. A half-open zero bound would expand to nothing (issue #111).
func parseListDateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	if fromStr == "" && toStr == "" {
		return time.Time{}, time.Time{}, nil
	}
	return parseDateRange(fromStr, toStr)
}

// parseCLIDate parses a YYYY-MM-DD flag value. It replaces time.Parse's
// verbose "cannot parse / out of range" surface with a
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

// mustPositiveDuration rejects a zero or negative duration after a
// successful parseCLIDuration call. Pass the flag name without the leading
// dashes and the raw flag value. The error names the flag and repeats the
// value, tagged with code "invalid_input".
func mustPositiveDuration(flag, raw string, d time.Duration) error {
	if d <= 0 {
		return errInvalidInputf("--%s must be positive (e.g. 30m, 1h), got %q", flag, raw)
	}
	return nil
}
