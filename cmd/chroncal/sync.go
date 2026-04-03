package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/auth"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync calendars with CalDAV servers",
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
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

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

			for _, s := range statuses {
				lastSync := "never"
				if s.LastSyncAt != "" {
					lastSync = s.LastSyncAt
				}
				lastAttempt := "never"
				if s.LastSyncAttemptedAt != "" {
					lastAttempt = s.LastSyncAttemptedAt
				}
				lastError := "-"
				if s.LastSyncError != "" {
					lastError = s.LastSyncError
				}
				fmt.Printf("  %-20s  account=%-15s  last_sync=%s  last_attempt=%s  pending=%d  conflicts=%d  last_error=%s\n",
					s.CalendarName, s.AccountName, lastSync, lastAttempt, s.PendingPush, s.Conflicts, lastError)
			}
			return nil
		},
	}
}

func syncConflictsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "conflicts",
		Short: "List unresolved sync conflicts",
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

			for _, c := range conflicts {
				fmt.Printf("  #%d  type=%s  uid=%s  detected=%s\n",
					c.ID, c.OwnerType, c.UID, c.DetectedAt.Format("2006-01-02 15:04"))
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
		Args:  cobra.ExactArgs(1),
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
					fmt.Fprintf(os.Stderr, "reset %s: %v\n", c.Name, err)
					continue
				}
				fmt.Printf("Reset sync state for %q\n", c.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "Reset only this calendar")
	return cmd
}

func printSyncResult(r *syncPkg.SyncResult) {
	fmt.Printf("  Calendar %d: pushed=%d pulled=%d deleted=%d conflicts=%d errors=%d\n",
		r.CalendarID, r.Pushed, r.Pulled, r.Deleted, r.Conflicts, len(r.Errors))
	for _, e := range r.Errors {
		fmt.Fprintf(os.Stderr, "    error: %v\n", e)
	}
}
