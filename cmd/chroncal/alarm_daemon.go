package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
)

func alarmDaemonCmd() *cobra.Command {
	var interval string
	var flagPolicy alarmExecutionPolicy
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run alarm check in a loop",
		Long: `Run "alarm check" repeatedly on a fixed interval.

The daemon performs an immediate check on startup, then sleeps for the
configured interval before checking again. It handles SIGINT and SIGTERM
for graceful shutdown.

For production use, prefer a systemd timer or cron job that runs
"chroncal alarm check" instead of a long-running daemon:

  # systemd timer (runs every 30 seconds)
  [Timer]
  OnBootSec=10s
  OnUnitActiveSec=30s

  [Service]
  ExecStart=/usr/local/bin/chroncal alarm check

See "chroncal alarm check --help" for notification types and SMTP configuration.`,
		Example: `  # Run with default 30-second interval
  chroncal alarm daemon

  # Check every minute
  chroncal alarm daemon --interval 1m

  # Check every 10 seconds
  chroncal alarm daemon --interval 10s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate before initApp so a bad interval fails before the
			// database opens, like the --days check in alarm missed.
			dur, err := parseCLIDuration("interval", interval)
			if err != nil {
				return err
			}
			if dur <= 0 {
				return errInvalidInputf("--interval must be a positive duration (e.g. 30s, 1m), got %q", interval)
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			w := cmd.OutOrStdout()
			fmt.Fprintf(os.Stderr, "chroncal: daemon started (interval: %s)\n", dur)
			policy := effectiveAlarmExecutionPolicy(cmd, flagPolicy)

			ticker := time.NewTicker(dur)
			defer ticker.Stop()

			// Run immediately on start, then on each tick. Returns an error
			// when the database is unhealthy: either Check itself failed, or
			// a fired alarm could not be marked (an unmarked alarm re-fires
			// every tick, so persistent mark failures mean notification spam).
			runCheck := func() error {
				return runDaemonTick(ctx, a, w, policy)
			}

			// A failed tick is usually transient (the TUI holding the write
			// lock past the busy timeout, a backup tool pinning the file), so
			// the threshold is generous — 30 ticks is 15 minutes at the
			// default interval. An unbroken run that long means the database
			// is gone, corrupt, or read-only, and silently retrying forever
			// would mask dead notifications (or spam re-fired ones).
			const maxConsecutiveTickFailures = 30
			consecutiveFailures := 0
			tick := func() error {
				if runCheck() != nil {
					consecutiveFailures++
					if consecutiveFailures >= maxConsecutiveTickFailures {
						return fmt.Errorf("alarm tick failed %d times in a row; giving up", consecutiveFailures)
					}
				} else {
					consecutiveFailures = 0
				}
				return nil
			}

			if err := tick(); err != nil {
				return err
			}

			for {
				select {
				case <-ctx.Done():
					fmt.Fprintf(os.Stderr, "chroncal: daemon stopped\n")
					return nil
				case <-ticker.C:
					if err := tick(); err != nil {
						return err
					}
				}
			}
		},
	}
	cmd.Flags().StringVar(&interval, "interval", "30s", "check interval (e.g. 30s, 1m)")
	bindAlarmExecutionPolicyFlags(cmd, &flagPolicy)
	return cmd
}

// runDaemonTick runs one daemon pass. It differs from runAlarmCheck on
// purpose: the daemon always writes text lines, emits no JSON records, and
// reports the first mark failure so the tick breaker can count it.
func runDaemonTick(ctx context.Context, a *app.App, w io.Writer, policy alarmExecutionPolicy) error {
	due, todoDue, err := a.Alarms.Check(ctx, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "chroncal: check error: %v\n", err)
		return err
	}
	var dbErr error
	for _, da := range due {
		res := markAndFireEventAlarm(ctx, a, da, policy)
		if res.MarkErr != nil {
			dbErr = res.MarkErr
		}
		if !res.Fired || res.FireErr != nil {
			continue
		}
		writeAlarmCheckLine(w, da.TriggerAt, da.Alarm.Action, da.Event.Title, false)
	}
	for _, tda := range todoDue {
		res := markAndFireTodoAlarm(ctx, a, tda, policy)
		if res.MarkErr != nil {
			dbErr = res.MarkErr
		}
		if !res.Fired || res.FireErr != nil {
			continue
		}
		writeAlarmCheckLine(w, tda.TriggerAt, tda.Alarm.Action, tda.Todo.Summary, true)
	}
	return dbErr
}
