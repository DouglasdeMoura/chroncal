package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// classifySyncError re-tags configuration-style sync failures (no remote
// link, no remote URL, credentials gone) as "invalid_input". JSON
// consumers can then distinguish "you have not set this up yet" from a genuine
// runtime sync failure. Match is by message substring. The
// internal/sync package returns plain fmt.Errorf chains. The alternative
// would be to export sentinel errors from that package.
func classifySyncError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "is not linked to an account"),
		strings.Contains(msg, "is not connected to a remote calendar"),
		strings.Contains(msg, "has no remote URL"),
		strings.Contains(msg, "get credentials:"):
		return &cliError{Code: "invalid_input", Msg: msg}
	}
	return err
}

// fprintImportWarnings prints one compact line per import warning collected
// on a SyncResult. The prefix tells users that sync produced it. Import
// warnings mark values the importer had to fabricate (a made-up DTEND span,
// a dropped alarm). The next push writes those back over the server's
// correct value. The silent sync entry points then print them to stderr.
// No output when there are none. The opportunistic push runs after every
// write.
func fprintImportWarnings(w io.Writer, warnings []syncPkg.ImportWarning) {
	for _, iw := range warnings {
		fmt.Fprintf(w, "import warning: %s\n", iw)
	}
}

// newSyncService builds the sync service for every sync subcommand. The
// app supplies the database, the domain services, and the credential
// store scope. A failed store construction returns an error; a command
// then fails with one clear message instead of a nil store that fails
// later with a confusing error. Pass a nil logger for silent commands.
// Pass an explicit logger when the command shows sync logs.
func newSyncService(a *app.App, logger *slog.Logger) (*syncPkg.Service, error) {
	credStore, err := auth.NewCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	return syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, logger), nil
}

func syncNewCalendars(
	ctx context.Context,
	a *app.App,
	store auth.CredentialStore,
	calendarIDs []int64,
	warnW io.Writer,
) error {
	if len(calendarIDs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, syncRunTimeout)
	defer cancel()
	svc := syncPkg.NewService(
		a.DB, a.Queries, store, a.Calendars, a.Events, a.Todos, a.Journals, nil,
	)
	var syncErrs []error
	for _, calendarID := range calendarIDs {
		result, err := svc.SyncCalendar(ctx, calendarID, syncPkg.ConflictServerWins)
		if result != nil {
			// The engine here runs with a discarded logger, so the result is
			// the only place the first pull's import warnings surface.
			fprintImportWarnings(warnW, result.Warnings)
		}
		if err == nil && len(result.Errors) > 0 {
			err = errors.Join(result.Errors...)
		}
		if err != nil {
			syncErrs = append(syncErrs, fmt.Errorf("initial sync for calendar %d: %w", calendarID, err))
		}
	}
	return errors.Join(syncErrs...)
}

const syncRunTimeout = 5 * time.Minute

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync connected calendars with CalDAV servers",
		Long: `Run manual sync operations, inspect sync state, and resolve
conflicts for calendars connected to remote CalDAV calendars.`,
		Example: `  chroncal sync run
  chroncal sync status
  chroncal sync conflicts`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(syncRunCmd(), syncStatusCmd(), syncConflictsCmd(), syncResolveCmd(), syncResetCmd(), syncDoctorCmd())
	return cmd
}

func syncRunCmd() *cobra.Command {
	var (
		calendarName string
		accountName  string
		conflict     string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run sync for one or all connected calendars",
		Long: `Push local changes and pull remote changes for connected calendars.

By default every connected calendar is synced. Use --calendar to limit the
run to a single local calendar, or --account to limit it to every calendar
linked to one CalDAV account. The two flags are mutually exclusive.`,
		Example: `  chroncal sync run
  chroncal sync run --calendar Work
  chroncal sync run --account Work
  chroncal sync run --conflict prompt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// An explicit --conflict wins. Without it, the configured
			// sync.conflict_strategy governs the run. Parse up front so a
			// typo (e.g. "Prompt", "ask") fails loudly instead of silently
			// falling back to server-wins and discarding local edits.
			// Mirrors the service.go check.
			strategyName := conflict
			if !cmd.Flags().Changed("conflict") {
				strategyName = cfg.Sync.ConflictStrategy
			}
			strategy, err := syncPkg.ParseConflictStrategy(strategyName)
			if err != nil {
				return errInvalidInputf("%s", err.Error())
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := context.WithTimeout(context.Background(), syncRunTimeout)
			defer cancel()

			// Look up names for every calendar up front so both the JSON and
			// text views can label results without re-querying per result.
			cals, err := a.Calendars.List(ctx)
			if err != nil {
				return err
			}
			calNames := make(map[int64]string, len(cals))
			for _, c := range cals {
				calNames[c.ID] = c.Name
			}

			// Resolve --calendar / --account before opening the credential
			// store. An unknown name is not_found; an account with no linked
			// calendars is a clean no-op. Neither needs a keyring, and CI has
			// none (see issue #474).
			var targetCalendarID, targetAccountID int64
			if calendarName != "" {
				target, err := findCalendarByRef(cals, calendarName)
				if err != nil {
					return &cliError{Code: "not_found", Msg: err.Error()}
				}
				targetCalendarID = target.ID
			} else if accountName != "" {
				acct, err := resolveAccount(ctx, a.Accounts, accountName)
				if err != nil {
					return err
				}
				targetAccountID = acct.ID
				linked := false
				for _, c := range cals {
					if c.AccountID == acct.ID {
						linked = true
						break
					}
				}
				if !linked {
					return renderSyncRunResults(cmd, nil, calNames)
				}
			}

			svc, err := newSyncService(a, stderrSyncLogger(os.Stderr))
			if err != nil {
				return err
			}

			var results []*syncPkg.SyncResult
			if targetCalendarID != 0 {
				r, err := svc.SyncCalendar(ctx, targetCalendarID, strategy)
				if err != nil {
					return classifySyncError(err)
				}
				results = []*syncPkg.SyncResult{r}
			} else if targetAccountID != 0 {
				// SyncAccount runs the account's calendars in series: they
				// share one credential, so a refresh must not race itself.
				results, err = svc.SyncAccount(ctx, targetAccountID, strategy)
				if err != nil {
					return classifySyncError(err)
				}
			} else {
				results, err = svc.SyncAll(ctx, strategy)
				if err != nil {
					return classifySyncError(err)
				}
			}

			return renderSyncRunResults(cmd, results, calNames)
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "Sync only this calendar")
	cmd.Flags().StringVar(&accountName, "account", "", "Sync only calendars linked to this account")
	cmd.Flags().StringVar(&conflict, "conflict", "server-wins", "Conflict strategy: server-wins or prompt (default: sync.conflict_strategy, else server-wins)")
	mutuallyExclusive(cmd, "calendar", "account")
	return cmd
}

func syncStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show sync status for all connected calendars",
		Long: `Show the last sync times, pending work, conflicts, and last error
for each connected calendar.`,
		Example: `  chroncal sync status
  chroncal sync status --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			svc, err := newSyncService(a, nil)
			if err != nil {
				return err
			}

			statuses, err := svc.Status(context.Background())
			if err != nil {
				return err
			}

			return renderSyncStatuses(cmd, statuses)
		},
	}
}

// renderSyncStatuses emits sync status using --output. For text mode an
// empty list returns the setup hint. JSON/YAML return [] so a script can
// branch on length rather than parse prose.
func renderSyncStatuses(cmd *cobra.Command, statuses []syncPkg.SyncStatus) error {
	w := cmd.OutOrStdout()

	if outputFmt != "text" {
		items := make([]map[string]any, 0, len(statuses))
		for _, s := range statuses {
			items = append(items, map[string]any{
				"calendar_id":            s.CalendarID,
				"calendar_name":          s.CalendarName,
				"last_sync_at":           s.LastSyncAt,
				"last_sync_attempted_at": s.LastSyncAttemptedAt,
				"last_sync_error":        s.LastSyncError,
				"pending_push":           s.PendingPush,
				"conflicts":              s.Conflicts,
			})
		}
		return printOutput(w, items)
	}

	if len(statuses) == 0 {
		fmt.Fprintln(w, "No connected calendars. Use 'chroncal calendar create ... --remote-url ...' or 'chroncal calendar update ... --remote-url ...' to set up sync.")
		return nil
	}
	for _, s := range statuses {
		writeSyncStatusLine(w, s)
	}
	return nil
}

func syncConflictsCmd() *cobra.Command {
	var resolved bool
	cmd := &cobra.Command{
		Use:   "conflicts",
		Short: "List sync conflicts awaiting resolution",
		Long: `List conflicts that need an explicit local-or-server decision.

Pass --resolved to list conflicts a sync pass or the user already resolved.
A resolved row keeps the recorded local version, so "chroncal sync resolve
<id> --pick local" can still restore that version.`,
		Example: `  chroncal sync conflicts
  chroncal sync conflicts --resolved
  chroncal sync conflicts --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			svc, err := newSyncService(a, nil)
			if err != nil {
				return err
			}

			var conflicts []syncPkg.Conflict
			if resolved {
				conflicts, err = svc.ListResolvedConflicts(context.Background())
			} else {
				conflicts, err = svc.ListConflicts(context.Background())
			}
			if err != nil {
				return err
			}

			return renderSyncConflicts(cmd, conflicts, resolved)
		},
	}
	cmd.Flags().BoolVar(&resolved, "resolved", false, "List resolved conflicts instead of unresolved ones")
	return cmd
}

// renderSyncConflicts emits conflicts using --output. DetectedAt and
// ResolvedAt are serialized as UTC RFC 3339 so JSON consumers get a
// stable, timezone-independent value.
func renderSyncConflicts(cmd *cobra.Command, conflicts []syncPkg.Conflict, resolved bool) error {
	w := cmd.OutOrStdout()

	if outputFmt != "text" {
		items := make([]map[string]any, 0, len(conflicts))
		for _, c := range conflicts {
			item := map[string]any{
				"id":          c.ID,
				"calendar_id": c.CalendarID,
				"owner_type":  c.OwnerType,
				"uid":         c.UID,
				"detected_at": c.DetectedAt.UTC().Format(time.RFC3339),
			}
			if c.Resolution != "" {
				item["resolution"] = c.Resolution
				item["resolved_at"] = c.ResolvedAt.UTC().Format(time.RFC3339)
			} else {
				item["resolution"] = nil
				item["resolved_at"] = nil
			}
			items = append(items, item)
		}
		return printOutput(w, items)
	}

	if len(conflicts) == 0 {
		if resolved {
			fmt.Fprintln(w, "No resolved conflicts.")
		} else {
			fmt.Fprintln(w, "No unresolved conflicts.")
		}
		return nil
	}
	for _, c := range conflicts {
		writeSyncConflictLine(w, c)
	}
	return nil
}

func syncResolveCmd() *cobra.Command {
	var pick string
	cmd := &cobra.Command{
		Use:   "resolve <id>",
		Short: "Resolve a sync conflict",
		Long: `Resolve a listed sync conflict by choosing which version wins.

Use "chroncal sync conflicts" first to find the conflict ID. A resolved
conflict stays listed under "chroncal sync conflicts --resolved"; resolving
it again with --pick local restores the recorded local version.`,
		Example: `  chroncal sync resolve 12 --pick local
  chroncal sync resolve 12 --pick server`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid conflict ID: %s", args[0])
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			svc, err := newSyncService(a, nil)
			if err != nil {
				return err
			}

			warnings, err := svc.ResolveConflict(context.Background(), id, pick)
			if err != nil {
				return err
			}
			// The service above runs with a nil (silent) engine logger, so the
			// returned warnings are the only place an accept-server import's
			// fabricated values surface. Stderr keeps them out of --output json.
			fprintImportWarnings(cmd.ErrOrStderr(), warnings)

			return renderSyncResolve(cmd, id, pick)
		},
	}
	cmd.Flags().StringVar(&pick, "pick", "", "Which version to keep: local or server (required)")
	if err := cmd.MarkFlagRequired("pick"); err != nil {
		panic(err)
	}
	return cmd
}

func syncResetCmd() *cobra.Command {
	var calendarName string
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Clear sync state and force a full re-sync",
		Long: `Forget stored sync cursors and conflict state so chroncal performs
a fresh sync on the next run.

This does not delete your local calendars or entries.`,
		Example: `  chroncal sync reset
  chroncal sync reset --calendar Work`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			svc, err := newSyncService(a, nil)
			if err != nil {
				return err
			}

			cals, err := a.Calendars.List(ctx)
			if err != nil {
				return err
			}

			var connected, failed int
			// Resolve --calendar by ID or case-insensitive name via the shared
			// findCalendarByRef helper. It already reports not_found for an
			// unknown reference, so a non-zero targetID is guaranteed to match a
			// calendar below.
			var targetID int64
			if calendarName != "" {
				target, err := findCalendarByRef(cals, calendarName)
				if err != nil {
					return &cliError{Code: "not_found", Msg: err.Error()}
				}
				targetID = target.ID
			}

			var outcomes []syncResetOutcome
			for _, c := range cals {
				if targetID != 0 && c.ID != targetID {
					continue
				}
				if c.AccountID == 0 {
					continue
				}
				connected++
				outcome := syncResetOutcome{Name: c.Name}
				if err := svc.ResetCalendar(ctx, c.ID); err != nil {
					failed++
					outcome.Err = err.Error()
				}
				outcomes = append(outcomes, outcome)
			}

			if calendarName != "" && connected == 0 {
				return &cliError{Code: "invalid_input", Msg: fmt.Sprintf("calendar %q is not connected to a remote; no sync state to reset", calendarName)}
			}
			if err := renderSyncReset(cmd, outcomes); err != nil {
				return err
			}
			if failed > 0 {
				return &cliError{Code: "error", Msg: fmt.Sprintf("failed to reset %d calendar(s)", failed)}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "Reset only this calendar")
	return cmd
}

// renderSyncRunResults emits per-calendar results plus a top-level summary,
// using the active --output format. A run with no connected calendars
// reports synced=0 rather than empty stdout. An agent can then
// distinguish "nothing to do" from "command crashed."
//
// Returns a non-nil error when any calendar reported per-phase sync errors.
// `sync run` then exits non-zero, consistent with `ical import` and
// `sync reset` (issue #359).
func renderSyncRunResults(cmd *cobra.Command, results []*syncPkg.SyncResult, calNames map[int64]string) error {
	w := cmd.OutOrStdout()

	if outputFmt != "text" {
		items := make([]map[string]any, 0, len(results))
		totalErrors := 0
		for _, r := range results {
			errMsgs := make([]string, 0, len(r.Errors))
			for _, e := range r.Errors {
				errMsgs = append(errMsgs, e.Error())
			}
			totalErrors += len(r.Errors)
			items = append(items, map[string]any{
				"calendar_id":       r.CalendarID,
				"calendar_name":     calNames[r.CalendarID],
				"pushed":            r.Pushed,
				"pulled":            r.Pulled,
				"deleted":           r.Deleted,
				"conflicts":         r.Conflicts,
				"auto_resolved":     r.AutoResolved,
				"skipped_conflicts": r.SkippedConflicts,
				"errors":            errMsgs,
			})
		}
		if err := printOutput(w, map[string]any{
			"synced":  len(results),
			"errors":  totalErrors,
			"results": items,
		}); err != nil {
			return err
		}
		if totalErrors > 0 {
			return &cliError{Code: "error", Msg: fmt.Sprintf("sync run completed with %d error(s)", totalErrors)}
		}
		return nil
	}

	if len(results) == 0 {
		fmt.Fprintln(w, "No connected calendars. Use 'chroncal calendar update <name> --remote-url ...' to set up sync.")
		return nil
	}
	totalErrors := 0
	for _, r := range results {
		writeSyncResult(w, cmd.ErrOrStderr(), r)
		totalErrors += len(r.Errors)
	}
	fmt.Fprintf(w, "Synced %d calendar(s).\n", len(results))
	if totalErrors > 0 {
		return &cliError{Code: "error", Msg: fmt.Sprintf("sync run completed with %d error(s)", totalErrors)}
	}
	return nil
}

// renderSyncResolve confirms a resolved conflict using the active --output
// format so machine consumers get JSON/YAML instead of the prose line.
func renderSyncResolve(cmd *cobra.Command, id int64, pick string) error {
	w := cmd.OutOrStdout()
	if outputFmt != "text" {
		return printOutput(w, map[string]any{
			"id":       id,
			"picked":   pick,
			"resolved": true,
		})
	}
	fmt.Fprintf(w, "Conflict #%d resolved (picked %s)\n", id, pick)
	return nil
}

// syncResetOutcome records the per-calendar result of a reset so both the
// text and JSON views render from the same data. Err is empty on success.
type syncResetOutcome struct {
	Name string
	Err  string
}

// renderSyncReset emits per-calendar reset results using --output. Text mode
// keeps failures on stderr; JSON/YAML fold them into each item's "error" so a
// script can read the whole batch from stdout.
func renderSyncReset(cmd *cobra.Command, outcomes []syncResetOutcome) error {
	w := cmd.OutOrStdout()
	if outputFmt != "text" {
		items := make([]map[string]any, 0, len(outcomes))
		for _, o := range outcomes {
			item := map[string]any{
				"calendar_name": o.Name,
				"reset":         o.Err == "",
			}
			if o.Err != "" {
				item["error"] = o.Err
			}
			items = append(items, item)
		}
		return printOutput(w, items)
	}
	for _, o := range outcomes {
		if o.Err != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "reset %s: %s\n", safeText(o.Name), safeText(o.Err))
			continue
		}
		fmt.Fprintf(w, "Reset sync state for %q\n", safeText(o.Name))
	}
	return nil
}

// stderrSyncLogger builds the terminal logger shared by the two subcommands
// that run sync engines in the foreground (`sync run` and `service run`).
// One constructor so a level or format change reaches both. The service
// tick is the copy nobody watches. It is the one that would drift.
func stderrSyncLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
