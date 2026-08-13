package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/auth"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

func tickCmd() *cobra.Command {
	var flagPolicy alarmExecutionPolicy
	cmd := &cobra.Command{
		Use:     "run",
		Aliases: []string{"tick"},
		Short:   "Run one background-service cycle: alarms always, sync when due",
		Long: `Run one background-service cycle.

Each run always checks alarms. It also runs sync for connected calendars
when the configured sync interval says a sync is due.`,
		Example: `  chroncal service run
  CHRONCAL_SYNC_INTERVAL=15m chroncal service run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTick(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), time.Now(), effectiveAlarmExecutionPolicy(cmd, flagPolicy))
		},
	}
	bindAlarmExecutionPolicyFlags(cmd, &flagPolicy)
	return cmd
}

func runTick(ctx context.Context, w, errW io.Writer, now time.Time, policy alarmExecutionPolicy) error {
	a, err := initApp()
	if err != nil {
		return err
	}
	defer a.Close()

	if err := runAlarmCheck(ctx, a, w, now, policy); err != nil {
		return err
	}

	interval, err := syncInterval()
	if err != nil {
		return err
	}
	if interval <= 0 {
		return nil
	}

	credStore, err := auth.NewCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
	if err != nil {
		return fmt.Errorf("credential store: %w", err)
	}
	// service run is a foreground process under systemd/launchd, so its
	// stderr goes to the journal — give the engine a real stderr logger like
	// `sync run` does, instead of the silent nil-logger default. This is the
	// primary background sync path: without it, import warnings
	// (logImportWarnings) and engine diagnostics vanish entirely.
	svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, stderrSyncLogger(errW))

	return runSyncPass(ctx, svc, now, interval, syncStrategy())
}

// tickSyncer is the slice of the sync service runSyncPass drives; an
// interface so tests can exercise the loop without a CalDAV server.
type tickSyncer interface {
	Status(ctx context.Context) ([]syncPkg.SyncStatus, error)
	SyncCalendar(ctx context.Context, calendarID int64, strategy syncPkg.ConflictStrategy) (*syncPkg.SyncResult, error)
}

// runSyncPass syncs every due calendar once. Per-phase errors recorded on a
// SyncResult count as failures even when SyncCalendar itself returned nil —
// discarding the result here used to leave a half-broken calendar reporting
// success (exit 0) on every tick.
func runSyncPass(ctx context.Context, svc tickSyncer, now time.Time, interval time.Duration, strategy syncPkg.ConflictStrategy) error {
	statuses, err := svc.Status(ctx)
	if err != nil {
		return fmt.Errorf("sync status: %w", err)
	}

	var syncErrs []error
	for _, status := range statuses {
		if !syncDue(now, status.LastSyncAttemptedAt, interval) {
			continue
		}
		result, err := svc.SyncCalendar(ctx, status.CalendarID, strategy)
		if err == nil && result != nil && len(result.Errors) > 0 {
			err = errors.Join(result.Errors...)
		}
		if err != nil {
			syncErrs = append(syncErrs, fmt.Errorf("%s: %w", status.CalendarName, err))
		}
	}
	if len(syncErrs) > 0 {
		return fmt.Errorf("service run sync failed: %w", errors.Join(syncErrs...))
	}
	return nil
}

func syncInterval() (time.Duration, error) {
	if cfg.Sync.Interval == "" {
		return 0, nil
	}
	dur, err := time.ParseDuration(cfg.Sync.Interval)
	if err != nil {
		return 0, fmt.Errorf("parse sync interval %q: %w", cfg.Sync.Interval, err)
	}
	return dur, nil
}

func syncStrategy() syncPkg.ConflictStrategy {
	if cfg.Sync.ConflictStrategy == string(syncPkg.ConflictPrompt) {
		return syncPkg.ConflictPrompt
	}
	return syncPkg.ConflictServerWins
}

func syncDue(now time.Time, lastAttempt string, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	if lastAttempt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, lastAttempt)
	if err != nil {
		return true
	}
	return !t.Add(interval).After(now)
}
