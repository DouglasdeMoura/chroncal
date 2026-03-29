package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/alarm"
	"github.com/douglasdemoura/tcal/internal/notify"
)

// fireAlarm dispatches the notification for a due alarm.
// EMAIL and AUDIO fall back to DISPLAY on failure.
func fireAlarm(da alarm.DueAlarm) error {
	switch da.Alarm.Action {
	case "AUDIO":
		if err := notify.Audio(da); err != nil {
			return notify.Display(da)
		}
		return nil
	case "EMAIL":
		smtpCfg := notify.SMTPConfig{
			Host:     cfg.SMTP.Host,
			Port:     cfg.SMTP.Port,
			Username: cfg.SMTP.Username,
			Password: cfg.SMTP.Password,
			From:     cfg.SMTP.From,
		}
		if err := notify.Email(da, smtpCfg); err != nil {
			return notify.Display(da)
		}
		return nil
	default: // DISPLAY and unknown
		return notify.Display(da)
	}
}

func alarmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alarm",
		Short: "Manage alarm notifications",
		Long: `Manage alarm notifications for calendar events.

Events can have one or more alarms attached (set via --alarm on event add/update).
The alarm lifecycle is:

  1. tcal alarm check   — scan events, fire notifications for due alarms
  2. tcal alarm list    — show fired alarms not yet acknowledged
  3. tcal alarm dismiss — acknowledge and clear a fired alarm
  4. tcal alarm snooze  — re-schedule a fired alarm for later

For continuous monitoring, use "tcal alarm daemon" or a systemd timer / cron job
that runs "tcal alarm check" on an interval.`,
	}
	cmd.AddCommand(alarmCheckCmd(), alarmListCmd(), alarmDismissCmd(), alarmSnoozeCmd(), alarmDaemonCmd())
	return cmd
}

func alarmCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Fire due alarms",
		Long: `Scan all events for alarms whose trigger time has passed and fire
notifications. Each alarm's trigger time is computed from the event's
start (or end) time plus the alarm's duration offset (e.g. -PT15M means
15 minutes before).

An alarm fires when its trigger time is in the past but within the last
24 hours (the stale threshold). Alarms older than 24 hours are silently
skipped to avoid a flood of stale notifications after downtime.

Notification types depend on the alarm action set on the event:
  DISPLAY  — desktop notification (default)
  AUDIO    — desktop notification + system alert sound
  EMAIL    — email via SMTP (falls back to DISPLAY if SMTP is not configured)

Each fired alarm is recorded in the database so it will not fire again on
subsequent checks. If no alarms are due, the command produces no output
and exits 0.`,
		Example: `  # One-shot check (suitable for cron / systemd timer)
  tcal alarm check

  # Check and output results as JSON
  tcal alarm check -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			due, err := a.Alarms.Check(ctx, time.Now())
			if err != nil {
				return fmt.Errorf("check alarms: %w", err)
			}

			w := cmd.OutOrStdout()

			if len(due) == 0 {
				if outputFmt != "text" {
					return printOutput(w, []any{})
				}
				return nil
			}

			var results []map[string]any
			for _, da := range due {
				fireErr := fireAlarm(da)

				if fireErr != nil {
					fmt.Fprintf(os.Stderr, "tcal: alarm error: %s (event=%q action=%s): %v\n",
						da.TriggerAt.Local().Format("15:04"), da.Event.Title, da.Alarm.Action, fireErr)
					if outputFmt != "text" {
						results = append(results, map[string]any{
							"event_id":   da.Event.ID,
							"event":      da.Event.Title,
							"alarm_id":   da.Alarm.ID,
							"action":     da.Alarm.Action,
							"trigger_at": da.TriggerAt.Format(time.RFC3339),
							"status":     fmt.Sprintf("error: %v", fireErr),
						})
					}
					continue
				}

				if da.StateID != 0 {
					// Re-fired snoozed alarm: update the existing state row.
					if markErr := a.Alarms.MarkRefired(ctx, da.StateID); markErr != nil {
						fmt.Fprintf(os.Stderr, "tcal: mark-refired error: event=%q: %v\n", da.Event.Title, markErr)
					}
				} else {
					if markErr := a.Alarms.MarkFired(ctx, da); markErr != nil {
						fmt.Fprintf(os.Stderr, "tcal: mark-fired error: event=%q: %v\n", da.Event.Title, markErr)
					}
				}

				if outputFmt != "text" {
					results = append(results, map[string]any{
						"event_id":   da.Event.ID,
						"event":      da.Event.Title,
						"alarm_id":   da.Alarm.ID,
						"action":     da.Alarm.Action,
						"trigger_at": da.TriggerAt.Format(time.RFC3339),
						"status":     "fired",
					})
				} else {
					fmt.Fprintf(w, "%s\t%s\t%s\n", da.TriggerAt.Local().Format("15:04"), da.Alarm.Action, da.Event.Title)
				}
			}

			if outputFmt != "text" {
				return printOutput(w, results)
			}
			return nil
		},
	}
	return cmd
}

func alarmListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List fired but unacknowledged alarms",
		Long: `Show all alarms that have fired but have not been dismissed.

Each entry includes a state ID that can be passed to "alarm dismiss" or
"alarm snooze". Snoozed alarms remain in the list with their snooze-until
time shown.`,
		Example: `  # List pending alarms
  tcal alarm list

  # List as JSON (useful for scripts)
  tcal alarm list -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			pending, err := a.Alarms.ListPending(ctx)
			if err != nil {
				return fmt.Errorf("list pending alarms: %w", err)
			}

			w := cmd.OutOrStdout()

			if len(pending) == 0 {
				if outputFmt != "text" {
					return printOutput(w, []any{})
				}
				return nil
			}

			if outputFmt != "text" {
				var items []map[string]any
				for _, s := range pending {
					items = append(items, map[string]any{
						"id":         s.ID,
						"alarm_id":   s.AlarmID,
						"event_id":   s.EventID,
						"trigger_at": s.TriggerAt,
						"fired_at":   s.FiredAt.String,
						"snoozed_to": s.SnoozedTo.String,
					})
				}
				return printOutput(w, items)
			}

			for _, s := range pending {
				snoozed := ""
				if s.SnoozedTo.Valid {
					snoozed = fmt.Sprintf(" (snoozed to %s)", s.SnoozedTo.String)
				}
				fmt.Fprintf(w, "  [%d] alarm=%d event=%d triggered=%s fired=%s%s\n",
					s.ID, s.AlarmID, s.EventID, s.TriggerAt, s.FiredAt.String, snoozed)
			}
			return nil
		},
	}
	return cmd
}

func alarmDismissCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dismiss <state-id>",
		Short: "Dismiss a fired alarm",
		Long: `Acknowledge a fired alarm so it no longer appears in "alarm list".

The state ID is shown in the output of "alarm list" (the number in
brackets). Dismissing an alarm marks it as acknowledged and is
permanent; use "alarm snooze" instead if you want to be reminded again
later.`,
		Example: `  # Dismiss alarm state #5
  tcal alarm dismiss 5`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			stateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid state ID: %w", err)
			}

			if err := a.Alarms.Dismiss(context.Background(), stateID); err != nil {
				return fmt.Errorf("dismiss alarm: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"dismissed": true, "id": stateID})
			}
			fmt.Fprintf(w, "Dismissed alarm state %d.\n", stateID)
			return nil
		},
	}
	return cmd
}

func alarmSnoozeCmd() *cobra.Command {
	var forDur string
	var untilStart bool
	cmd := &cobra.Command{
		Use:   "snooze <state-id>",
		Short: "Snooze a fired alarm",
		Long: `Postpone a fired alarm so it can fire again after a delay.

The snooze time is bounded by the event timeline: if the requested
duration would place the reminder after the event ends, it is
automatically capped to the event's end time. A warning is shown if
the reminder will fire after the event has already started.

Use --until-start to snooze until the moment the event begins.

The alarm remains in the pending list (shown by "alarm list") with the
snooze-until time recorded. When "alarm check" runs after the snooze
expires, the alarm fires again.`,
		Example: `  # Snooze for the default 15 minutes
  tcal alarm snooze 5

  # Snooze for 1 hour
  tcal alarm snooze 5 --for 1h

  # Snooze until the event starts
  tcal alarm snooze 5 --until-start`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			stateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid state ID: %w", err)
			}

			ctx := context.Background()
			w := cmd.OutOrStdout()

			var res alarm.SnoozeResult
			if untilStart {
				res, err = a.Alarms.SnoozeUntilStart(ctx, stateID)
				if err != nil {
					return fmt.Errorf("snooze until start: %w", err)
				}
			} else {
				dur, err := time.ParseDuration(forDur)
				if err != nil {
					return fmt.Errorf("parse --for duration: %w", err)
				}
				res, err = a.Alarms.ComputeSnooze(ctx, stateID, dur)
				if err != nil {
					return fmt.Errorf("compute snooze: %w", err)
				}
			}

			if err := a.Alarms.Snooze(ctx, stateID, res.Until); err != nil {
				return fmt.Errorf("snooze alarm: %w", err)
			}

			if outputFmt != "text" {
				return printOutput(w, map[string]any{
					"snoozed":    true,
					"id":         stateID,
					"until":      res.Until.Format(time.RFC3339),
					"capped":     res.Capped,
					"past_start": res.PastStart,
				})
			}

			if res.Capped {
				fmt.Fprintf(os.Stderr, "tcal: snooze capped at event end (%s)\n",
					res.EventEnd.Local().Format("15:04"))
			} else if res.PastStart {
				fmt.Fprintf(os.Stderr, "tcal: note: alarm will fire after event starts (%s)\n",
					res.EventStart.Local().Format("15:04"))
			}
			fmt.Fprintf(w, "Snoozed alarm state %d until %s.\n", stateID, res.Until.Local().Format("15:04"))
			return nil
		},
	}
	cmd.Flags().StringVar(&forDur, "for", "15m", "snooze duration (e.g. 15m, 1h)")
	cmd.Flags().BoolVar(&untilStart, "until-start", false, "snooze until the event starts")
	cmd.MarkFlagsMutuallyExclusive("for", "until-start")
	return cmd
}

func alarmDaemonCmd() *cobra.Command {
	var interval string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run alarm check in a loop",
		Long: `Run "alarm check" repeatedly on a fixed interval.

The daemon performs an immediate check on startup, then sleeps for the
configured interval before checking again. It handles SIGINT and SIGTERM
for graceful shutdown.

For production use, prefer a systemd timer or cron job that runs
"tcal alarm check" instead of a long-running daemon:

  # systemd timer (runs every 30 seconds)
  [Timer]
  OnBootSec=10s
  OnUnitActiveSec=30s

  [Service]
  ExecStart=/usr/local/bin/tcal alarm check`,
		Example: `  # Run with default 30-second interval
  tcal alarm daemon

  # Check every minute
  tcal alarm daemon --interval 1m

  # Check every 10 seconds
  tcal alarm daemon --interval 10s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			dur, err := time.ParseDuration(interval)
			if err != nil {
				return fmt.Errorf("parse --interval: %w", err)
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			w := cmd.OutOrStdout()
			fmt.Fprintf(os.Stderr, "tcal: daemon started (interval: %s)\n", dur)

			ticker := time.NewTicker(dur)
			defer ticker.Stop()

			// Run immediately on start, then on each tick.
			runCheck := func() {
				due, err := a.Alarms.Check(ctx, time.Now())
				if err != nil {
					fmt.Fprintf(os.Stderr, "tcal: check error: %v\n", err)
					return
				}
				for _, da := range due {
					fireErr := fireAlarm(da)

					if fireErr != nil {
						fmt.Fprintf(os.Stderr, "tcal: alarm error: %s (event=%q action=%s): %v\n",
							da.TriggerAt.Local().Format("15:04"), da.Event.Title, da.Alarm.Action, fireErr)
						continue
					}

					if da.StateID != 0 {
						if markErr := a.Alarms.MarkRefired(ctx, da.StateID); markErr != nil {
							fmt.Fprintf(os.Stderr, "tcal: mark-refired error: event=%q: %v\n", da.Event.Title, markErr)
						}
					} else {
						if markErr := a.Alarms.MarkFired(ctx, da); markErr != nil {
							fmt.Fprintf(os.Stderr, "tcal: mark-fired error: event=%q: %v\n", da.Event.Title, markErr)
						}
					}

					fmt.Fprintf(w, "%s\t%s\t%s\n", da.TriggerAt.Local().Format("15:04"), da.Alarm.Action, da.Event.Title)
				}
			}

			runCheck()

			for {
				select {
				case <-ctx.Done():
					fmt.Fprintf(os.Stderr, "tcal: daemon stopped\n")
					return nil
				case <-ticker.C:
					runCheck()
				}
			}
		},
	}
	cmd.Flags().StringVar(&interval, "interval", "30s", "check interval (e.g. 30s, 1m)")
	return cmd
}
