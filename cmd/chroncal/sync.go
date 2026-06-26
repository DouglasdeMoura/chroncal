package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/auth"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// classifySyncError re-tags configuration-style sync failures (no remote
// link, no remote URL, missing credentials) as "invalid_input" so JSON
// consumers can distinguish "you haven't set this up yet" from a genuine
// runtime sync failure. Matching is by message substring because the
// internal/sync package returns plain fmt.Errorf chains; the alternative
// would be exporting sentinel errors from that package.
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
	cmd.AddCommand(syncRunCmd(), syncStatusCmd(), syncConflictsCmd(), syncResolveCmd(), syncResetCmd())
	return cmd
}

func syncRunCmd() *cobra.Command {
	var (
		calendarName string
		conflict     string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run sync for one or all connected calendars",
		Long: `Push local changes and pull remote changes for connected calendars.

By default every connected calendar is synced. Use --calendar to limit the
run to a single local calendar.`,
		Example: `  chroncal sync run
  chroncal sync run --calendar Work
  chroncal sync run --conflict prompt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := context.WithTimeout(context.Background(), syncRunTimeout)
			defer cancel()

			credStore, err := auth.NewCredentialStore(true)
			if err != nil {
				return fmt.Errorf("credential store: %w", err)
			}

			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, logger)

			strategy := syncPkg.ConflictServerWins
			if conflict == "prompt" {
				strategy = syncPkg.ConflictPrompt
			}

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

			var results []*syncPkg.SyncResult
			if calendarName != "" {
				var calID int64
				for _, c := range cals {
					if c.Name == calendarName {
						calID = c.ID
						break
					}
				}
				if calID == 0 {
					return &cliError{Code: "not_found", Msg: fmt.Sprintf("calendar %q not found", calendarName)}
				}
				r, err := svc.SyncCalendar(ctx, calID, strategy)
				if err != nil {
					return classifySyncError(err)
				}
				results = []*syncPkg.SyncResult{r}
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
	cmd.Flags().StringVar(&conflict, "conflict", "server-wins", "Conflict strategy: server-wins or prompt")
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

			credStore, _ := auth.NewCredentialStore(true)
			svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, nil)

			statuses, err := svc.Status(context.Background())
			if err != nil {
				return err
			}

			return renderSyncStatuses(cmd, statuses)
		},
	}
}

// renderSyncStatuses emits sync status using --output. For text mode an
// empty list returns the setup hint; JSON/YAML return [] so a script can
// branch on length rather than parsing prose.
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
	return &cobra.Command{
		Use:   "conflicts",
		Short: "List unresolved sync conflicts",
		Long:  `List conflicts that need an explicit local-or-server decision.`,
		Example: `  chroncal sync conflicts
  chroncal sync conflicts --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			credStore, _ := auth.NewCredentialStore(true)
			svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, nil)

			conflicts, err := svc.ListConflicts(context.Background())
			if err != nil {
				return err
			}

			return renderSyncConflicts(cmd, conflicts)
		},
	}
}

// renderSyncConflicts emits unresolved conflicts using --output.
// DetectedAt is serialized as UTC RFC 3339 so JSON consumers get a
// stable, timezone-independent value.
func renderSyncConflicts(cmd *cobra.Command, conflicts []syncPkg.Conflict) error {
	w := cmd.OutOrStdout()

	if outputFmt != "text" {
		items := make([]map[string]any, 0, len(conflicts))
		for _, c := range conflicts {
			items = append(items, map[string]any{
				"id":          c.ID,
				"calendar_id": c.CalendarID,
				"owner_type":  c.OwnerType,
				"uid":         c.UID,
				"detected_at": c.DetectedAt.UTC().Format(time.RFC3339),
			})
		}
		return printOutput(w, items)
	}

	if len(conflicts) == 0 {
		fmt.Fprintln(w, "No unresolved conflicts.")
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

Use "chroncal sync conflicts" first to find the conflict ID.`,
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

			credStore, _ := auth.NewCredentialStore(true)
			svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, nil)

			if err := svc.ResolveConflict(context.Background(), id, pick); err != nil {
				return err
			}

			fmt.Printf("Conflict #%d resolved (picked %s)\n", id, pick)
			return nil
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

			credStore, _ := auth.NewCredentialStore(true)
			svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, nil)

			cals, err := a.Calendars.List(ctx)
			if err != nil {
				return err
			}

			var nameFound, connected, failed int
			for _, c := range cals {
				if calendarName != "" && c.Name != calendarName {
					continue
				}
				nameFound++
				if c.AccountID == 0 {
					continue
				}
				connected++
				if err := svc.ResetCalendar(ctx, c.ID); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "reset %s: %s\n", safeText(c.Name), safeText(err.Error()))
					continue
				}
				fmt.Printf("Reset sync state for %q\n", safeText(c.Name))
			}

			if calendarName != "" {
				if nameFound == 0 {
					return &cliError{Code: "not_found", Msg: fmt.Sprintf("calendar %q not found", calendarName)}
				}
				if connected == 0 {
					return &cliError{Code: "invalid_input", Msg: fmt.Sprintf("calendar %q is not connected to a remote; no sync state to reset", calendarName)}
				}
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
// reports synced=0 rather than producing empty stdout so an agent can
// distinguish "nothing to do" from "command crashed."
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
				"calendar_id":   r.CalendarID,
				"calendar_name": calNames[r.CalendarID],
				"pushed":        r.Pushed,
				"pulled":        r.Pulled,
				"deleted":       r.Deleted,
				"conflicts":     r.Conflicts,
				"errors":        errMsgs,
			})
		}
		return printOutput(w, map[string]any{
			"synced":  len(results),
			"errors":  totalErrors,
			"results": items,
		})
	}

	if len(results) == 0 {
		fmt.Fprintln(w, "No connected calendars. Use 'chroncal calendar update <name> --remote-url ...' to set up sync.")
		return nil
	}
	for _, r := range results {
		writeSyncResult(w, cmd.ErrOrStderr(), r)
	}
	fmt.Fprintf(w, "Synced %d calendar(s).\n", len(results))
	return nil
}
