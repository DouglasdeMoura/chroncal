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
	}
	cmd.AddCommand(alarmCheckCmd(), alarmListCmd(), alarmDismissCmd(), alarmSnoozeCmd(), alarmDaemonCmd())
	return cmd
}

func alarmCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Fire due alarms",
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
				if jsonOut {
					return printJSON(w, []any{})
				}
				return nil
			}

			var results []map[string]any
			for _, da := range due {
				fireErr := fireAlarm(da)

				if fireErr != nil {
					fmt.Fprintf(os.Stderr, "tcal: alarm error: %s (event=%q action=%s): %v\n",
						da.TriggerAt.Local().Format("15:04"), da.Event.Title, da.Alarm.Action, fireErr)
					if jsonOut {
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

				if markErr := a.Alarms.MarkFired(ctx, da); markErr != nil {
					fmt.Fprintf(os.Stderr, "tcal: mark-fired error: event=%q: %v\n", da.Event.Title, markErr)
				}

				if jsonOut {
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

			if jsonOut {
				return printJSON(w, results)
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
				if jsonOut {
					return printJSON(w, []any{})
				}
				return nil
			}

			if jsonOut {
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
				return printJSON(w, items)
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
			if jsonOut {
				return printJSON(w, map[string]any{"dismissed": true, "id": stateID})
			}
			fmt.Fprintf(w, "Dismissed alarm state %d.\n", stateID)
			return nil
		},
	}
	return cmd
}

func alarmSnoozeCmd() *cobra.Command {
	var forDur string
	cmd := &cobra.Command{
		Use:   "snooze <state-id>",
		Short: "Snooze a fired alarm",
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

			dur, err := time.ParseDuration(forDur)
			if err != nil {
				return fmt.Errorf("parse --for duration: %w", err)
			}

			until := time.Now().Add(dur)
			if err := a.Alarms.Snooze(context.Background(), stateID, until); err != nil {
				return fmt.Errorf("snooze alarm: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, map[string]any{
					"snoozed": true,
					"id":      stateID,
					"until":   until.Format(time.RFC3339),
				})
			}
			fmt.Fprintf(w, "Snoozed alarm state %d until %s.\n", stateID, until.Local().Format("15:04"))
			return nil
		},
	}
	cmd.Flags().StringVar(&forDur, "for", "15m", "snooze duration (e.g. 15m, 1h)")
	return cmd
}

func alarmDaemonCmd() *cobra.Command {
	var interval string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run alarm check in a loop",
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

					if markErr := a.Alarms.MarkFired(ctx, da); markErr != nil {
						fmt.Fprintf(os.Stderr, "tcal: mark-fired error: event=%q: %v\n", da.Event.Title, markErr)
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
