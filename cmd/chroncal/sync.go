package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/auth"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

const syncRunTimeout = 5 * time.Minute

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync linked calendars with CalDAV servers",
		Long: `Run manual sync operations, inspect sync state, and resolve
conflicts for calendars linked to remote CalDAV accounts.`,
		Example: `  chroncal sync run
  chroncal sync status
  chroncal sync conflicts`,
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
		Short: "Run sync for one or all linked calendars",
		Long: `Push local changes and pull remote changes for linked calendars.

By default every linked calendar is synced. Use --calendar to limit the
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

			if calendarName != "" {
				// Resolve calendar by name
				cals, err := a.Calendars.List(ctx)
				if err != nil {
					return err
				}
				var calID int64
				for _, c := range cals {
					if c.Name == calendarName {
						calID = c.ID
						break
					}
				}
				if calID == 0 {
					return fmt.Errorf("calendar %q not found", calendarName)
				}

				result, err := svc.SyncCalendar(ctx, calID, strategy)
				if err != nil {
					return err
				}
				printSyncResult(result)
			} else {
				results, err := svc.SyncAll(ctx, strategy)
				if err != nil {
					return err
				}
				for _, r := range results {
					printSyncResult(r)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "Sync only this calendar")
	cmd.Flags().StringVar(&conflict, "conflict", "server-wins", "Conflict strategy: server-wins or prompt")
	return cmd
}

func syncStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show sync status for all linked calendars",
		Long: `Show the last sync times, pending work, conflicts, and last error
for each linked calendar.`,
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

			if len(statuses) == 0 {
				fmt.Println("No synced calendars. Use 'chroncal account add' to set up sync.")
				return nil
			}

			w := cmd.OutOrStdout()
			for _, s := range statuses {
				writeSyncStatusLine(w, s)
			}
			return nil
		},
	}
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

			if len(conflicts) == 0 {
				fmt.Println("No unresolved conflicts.")
				return nil
			}

			w := cmd.OutOrStdout()
			for _, c := range conflicts {
				writeSyncConflictLine(w, c)
			}
			return nil
		},
	}
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
		Args: cobra.ExactArgs(1),
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
	cmd.MarkFlagRequired("pick")
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

			for _, c := range cals {
				if calendarName != "" && c.Name != calendarName {
					continue
				}
				if c.AccountID == 0 {
					continue
				}
				if err := svc.ResetCalendar(ctx, c.ID); err != nil {
					fmt.Fprintf(os.Stderr, "reset %s: %s\n", safeText(c.Name), safeText(err.Error()))
					continue
				}
				fmt.Printf("Reset sync state for %q\n", safeText(c.Name))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "Reset only this calendar")
	return cmd
}

func printSyncResult(r *syncPkg.SyncResult) {
	writeSyncResult(os.Stdout, os.Stderr, r)
}
