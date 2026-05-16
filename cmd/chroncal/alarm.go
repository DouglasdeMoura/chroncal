package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/alarm"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/notify"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// parseStateID parses a state ID string that may be prefixed with "t" for todo alarms.
// Returns the numeric ID and whether it's a todo alarm.
func parseStateID(s string) (int64, bool, error) {
	if strings.HasPrefix(s, "t") || strings.HasPrefix(s, "T") {
		id, err := strconv.ParseInt(s[1:], 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("invalid todo state ID %q", s)
		}
		return id, true, nil
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid state ID %q (use 't<N>' for todo alarms)", s)
	}
	return id, false, nil
}

// fireAlarm dispatches the notification for a due alarm.
// EMAIL and AUDIO fall back to DISPLAY on failure.
//
// RFC 5545 REPEAT and DURATION on VALARM specify additional
// post-trigger notifications. The alarm check loop generates separate
// trigger times for each repeat, each tracked independently via alarm state.
func fireAlarm(da alarm.DueAlarm, policy alarmExecutionPolicy) error {
	switch da.Alarm.Action {
	case "AUDIO":
		if err := notify.Audio(da, policy.notifyPolicy()); err != nil {
			return notify.Display(da)
		}
		return nil
	case "EMAIL":
		if err := notify.Email(da, cfg.SMTP, policy.notifyPolicy()); err != nil {
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

  1. chroncal alarm check   — scan events, fire notifications for due alarms
  2. chroncal alarm list    — show fired alarms not yet acknowledged
  3. chroncal alarm dismiss — acknowledge and clear a fired alarm
  4. chroncal alarm snooze  — re-schedule a fired alarm for later

For continuous monitoring, use "chroncal alarm daemon" or a systemd timer / cron job
that runs "chroncal alarm check" on an interval.`,
		Example: `  chroncal alarm check
  chroncal alarm list
  chroncal alarm snooze 12 --for 10m
  chroncal alarm daemon`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(alarmCheckCmd(), alarmListCmd(), alarmDismissCmd(), alarmSnoozeCmd(), alarmDaemonCmd(), alarmMissedCmd())
	return cmd
}

func alarmCheckCmd() *cobra.Command {
	var flagPolicy alarmExecutionPolicy
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
  EMAIL    — email via SMTP when configured (falls back to DISPLAY otherwise)

To enable EMAIL notifications, configure SMTP via environment variables:
  CHRONCAL_SMTP_HOST       SMTP server hostname (required)
  CHRONCAL_SMTP_PORT       SMTP server port (default: 587)
  CHRONCAL_SMTP_USERNAME   SMTP authentication username
  CHRONCAL_SMTP_PASSWORD   SMTP authentication password
  CHRONCAL_SMTP_FROM       sender address for alarm emails

Or in the config file ($XDG_CONFIG_HOME/chroncal/config.toml):
  [smtp]
  host = "smtp.example.com"
  port = 587
  username = "user@example.com"
  password = "app-password"
  from = "noreply@example.com"

Environment variables override config file values.

Each fired alarm is recorded in the database so it will not fire again on
subsequent checks. Snoozed alarms whose snooze-until time has expired
are also re-fired. If no alarms are due, the command produces no output
and exits 0.`,
		Example: `  # One-shot check (suitable for cron / systemd timer)
  chroncal alarm check

  # Check and output results as JSON
  chroncal alarm check -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			return runAlarmCheck(context.Background(), a, cmd.OutOrStdout(), time.Now(), effectiveAlarmExecutionPolicy(cmd, flagPolicy))
		},
	}
	bindAlarmExecutionPolicyFlags(cmd, &flagPolicy)
	return cmd
}

func runAlarmCheck(ctx context.Context, a *app.App, w io.Writer, now time.Time, policy alarmExecutionPolicy) error {
	due, todoDue, err := a.Alarms.Check(ctx, now)
	if err != nil {
		return fmt.Errorf("check alarms: %w", err)
	}

	if len(due) == 0 && len(todoDue) == 0 {
		if outputFmt != "text" {
			return printOutput(w, []any{})
		}
		return nil
	}

	var results []map[string]any
	for _, da := range due {
		stateID := da.StateID
		if stateID != 0 {
			if markErr := a.Alarms.MarkRefired(ctx, stateID); markErr != nil {
				fmt.Fprintf(os.Stderr, "chroncal: mark-refired error: event=%q: %v\n", safeText(da.Event.Title), markErr)
			}
		} else {
			newID, markErr := a.Alarms.MarkFired(ctx, da)
			if markErr != nil {
				fmt.Fprintf(os.Stderr, "chroncal: mark-fired error: event=%q: %v\n", safeText(da.Event.Title), markErr)
			} else {
				stateID = newID
			}
		}

		fireErr := fireAlarm(da, policy)
		if fireErr != nil {
			fmt.Fprintf(os.Stderr, "chroncal: alarm error: %s (event=%q action=%s): %v\n",
				da.TriggerAt.Local().Format("15:04"), safeText(da.Event.Title), da.Alarm.Action, fireErr)
			if outputFmt != "text" {
				results = append(results, map[string]any{
					"event_id":   da.Event.ID,
					"event":      da.Event.Title,
					"alarm_id":   da.Alarm.ID,
					"state_id":   stateID,
					"action":     da.Alarm.Action,
					"trigger_at": da.TriggerAt.Format(time.RFC3339),
					"status":     fmt.Sprintf("error: %v", fireErr),
				})
			}
			continue
		}

		if outputFmt != "text" {
			results = append(results, map[string]any{
				"event_id":   da.Event.ID,
				"event":      da.Event.Title,
				"alarm_id":   da.Alarm.ID,
				"state_id":   stateID,
				"action":     da.Alarm.Action,
				"trigger_at": da.TriggerAt.Format(time.RFC3339),
				"status":     "fired",
			})
		} else {
			writeAlarmCheckLine(w, da.TriggerAt, da.Alarm.Action, da.Event.Title, false)
		}
	}

	for _, tda := range todoDue {
		stateID := tda.StateID
		if stateID != 0 {
			if markErr := a.Alarms.MarkTodoRefired(ctx, stateID); markErr != nil {
				fmt.Fprintf(os.Stderr, "chroncal: mark-refired error: todo=%q: %v\n", safeText(tda.Todo.Summary), markErr)
			}
		} else {
			newID, markErr := a.Alarms.MarkTodoFired(ctx, tda)
			if markErr != nil {
				fmt.Fprintf(os.Stderr, "chroncal: mark-fired error: todo=%q: %v\n", safeText(tda.Todo.Summary), markErr)
			} else {
				stateID = newID
			}
		}

		fireErr := fireAlarm(todoDueAlarmToDueAlarm(tda), policy)
		if fireErr != nil {
			fmt.Fprintf(os.Stderr, "chroncal: todo alarm error: %s (todo=%q action=%s): %v\n",
				tda.TriggerAt.Local().Format("15:04"), safeText(tda.Todo.Summary), tda.Alarm.Action, fireErr)
			if outputFmt != "text" {
				results = append(results, map[string]any{
					"todo_id":    tda.Todo.ID,
					"todo":       tda.Todo.Summary,
					"alarm_id":   tda.Alarm.ID,
					"state_id":   stateID,
					"action":     tda.Alarm.Action,
					"trigger_at": tda.TriggerAt.Format(time.RFC3339),
					"status":     fmt.Sprintf("error: %v", fireErr),
				})
			}
			continue
		}

		if outputFmt != "text" {
			results = append(results, map[string]any{
				"todo_id":    tda.Todo.ID,
				"todo":       tda.Todo.Summary,
				"alarm_id":   tda.Alarm.ID,
				"state_id":   stateID,
				"action":     tda.Alarm.Action,
				"trigger_at": tda.TriggerAt.Format(time.RFC3339),
				"status":     "fired",
			})
		} else {
			writeAlarmCheckLine(w, tda.TriggerAt, tda.Alarm.Action, tda.Todo.Summary, true)
		}
	}

	if outputFmt != "text" {
		return printOutput(w, results)
	}
	return nil
}

func alarmListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List fired but unacknowledged alarms",
		Long: `Show all alarms that have fired but have not been dismissed.

Alarms enter this list when "chroncal alarm check" (or "chroncal alarm daemon")
detects that an alarm's trigger time has passed and fires a notification.
Once fired, an alarm stays in the pending list until you dismiss it.

Both event and todo alarms are shown. Todo alarm IDs are prefixed with "t"
(e.g. [t3]) to distinguish them from event alarm IDs (e.g. [3]). Use the
prefixed form with "alarm dismiss" and "alarm snooze".

Text output columns:
  [ID]  TRIGGER_TIME  ACTION  TITLE  (snoozed to HH:MM)

JSON output fields (-o json):
  id, type, alarm_id, event_id/todo_id, title, action, trigger_at, fired_at, snoozed_to

Dismissed alarms are permanently removed from this list.`,
		Example: `  # List pending alarms
  chroncal alarm list

  # List as JSON (useful for scripts)
  chroncal alarm list -o json

  # Typical workflow: check for due alarms, review, then act
  chroncal alarm check          # fire any due alarms
  chroncal alarm list           # see what fired
  chroncal alarm dismiss 5      # clear event alarm state #5
  chroncal alarm dismiss t3     # clear todo alarm state #3
  chroncal alarm snooze 3       # remind again in 15 minutes`,
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
			pendingTodos, err := a.Alarms.ListPendingTodoAlarms(ctx)
			if err != nil {
				return fmt.Errorf("list pending todo alarms: %w", err)
			}

			w := cmd.OutOrStdout()

			if len(pending) == 0 && len(pendingTodos) == 0 {
				if outputFmt != "text" {
					return printOutput(w, []any{})
				}
				fmt.Fprintln(w, "No pending alarms found.")
				return nil
			}

			// Enrich each event alarm state with title and action.
			type pendingInfo struct {
				ID     string // display ID: "3" or "t3"
				State  storage.AlarmState
				Title  string
				Action string
			}
			var enriched []pendingInfo
			for _, s := range pending {
				info := pendingInfo{
					ID:    fmt.Sprintf("%d", s.ID),
					State: s,
					Title: fmt.Sprintf("event#%d", s.EventID),
				}
				if evt, err := a.Events.Get(ctx, s.EventID); err == nil {
					info.Title = evt.Title
					if alarms, err := a.Events.ListAlarms(ctx, evt.ID); err == nil {
						for _, al := range alarms {
							if al.ID == s.AlarmID {
								info.Action = al.Action
								break
							}
						}
					}
				}
				enriched = append(enriched, info)
			}

			// Enrich each todo alarm state.
			type pendingTodoInfo struct {
				ID     string // display ID: "t3"
				State  storage.TodoAlarmState
				Title  string
				Action string
			}
			var enrichedTodos []pendingTodoInfo
			for _, s := range pendingTodos {
				info := pendingTodoInfo{
					ID:    fmt.Sprintf("t%d", s.ID),
					State: s,
					Title: fmt.Sprintf("todo#%d", s.TodoID),
				}
				if td, err := a.Todos.Get(ctx, s.TodoID); err == nil {
					info.Title = td.Summary
					if alarms, err := a.Todos.ListAlarms(ctx, td.ID); err == nil {
						for _, al := range alarms {
							if al.ID == s.AlarmID {
								info.Action = al.Action
								break
							}
						}
					}
				}
				enrichedTodos = append(enrichedTodos, info)
			}

			if outputFmt != "text" {
				var items []map[string]any
				for _, p := range enriched {
					items = append(items, map[string]any{
						"id":         p.ID,
						"type":       "event",
						"alarm_id":   p.State.AlarmID,
						"event_id":   p.State.EventID,
						"title":      p.Title,
						"action":     p.Action,
						"trigger_at": p.State.TriggerAt,
						"fired_at":   storage.NullableToString(p.State.FiredAt),
						"snoozed_to": storage.NullableToString(p.State.SnoozedTo),
					})
				}
				for _, p := range enrichedTodos {
					items = append(items, map[string]any{
						"id":         p.ID,
						"type":       "todo",
						"alarm_id":   p.State.AlarmID,
						"todo_id":    p.State.TodoID,
						"title":      p.Title,
						"action":     p.Action,
						"trigger_at": p.State.TriggerAt,
						"fired_at":   storage.NullableToString(p.State.FiredAt),
						"snoozed_to": storage.NullableToString(p.State.SnoozedTo),
					})
				}
				return printOutput(w, items)
			}

			for _, p := range enriched {
				triggerLocal := p.State.TriggerAt
				if t, err := time.Parse(time.RFC3339, p.State.TriggerAt); err == nil {
					triggerLocal = t.Local().Format("2006-01-02 15:04")
				}
				snoozed := ""
				if p.State.SnoozedTo != nil {
					snz := *p.State.SnoozedTo
					if t, err := time.Parse(time.RFC3339, snz); err == nil {
						snz = t.Local().Format("15:04")
					}
					snoozed = fmt.Sprintf(" (snoozed to %s)", snz)
				}
				writePendingAlarmLine(w, p.ID, triggerLocal, p.Action, p.Title, false, snoozed)
			}
			for _, p := range enrichedTodos {
				triggerLocal := p.State.TriggerAt
				if t, err := time.Parse(time.RFC3339, p.State.TriggerAt); err == nil {
					triggerLocal = t.Local().Format("2006-01-02 15:04")
				}
				snoozed := ""
				if p.State.SnoozedTo != nil {
					snz := *p.State.SnoozedTo
					if t, err := time.Parse(time.RFC3339, snz); err == nil {
						snz = t.Local().Format("15:04")
					}
					snoozed = fmt.Sprintf(" (snoozed to %s)", snz)
				}
				writePendingAlarmLine(w, p.ID, triggerLocal, p.Action, p.Title, true, snoozed)
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
later.

For todo alarms, use the "t" prefix shown in "alarm list" (e.g. t3).`,
		Example: `  # Dismiss event alarm state #5
  chroncal alarm dismiss 5

  # Dismiss todo alarm state #3
  chroncal alarm dismiss t3`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			stateID, isTodo, err := parseStateID(args[0])
			if err != nil {
				return err
			}

			ctx := context.Background()
			if isTodo {
				if err := a.Alarms.DismissTodoAlarm(ctx, stateID); err != nil {
					return fmt.Errorf("dismiss todo alarm: %w", err)
				}
			} else {
				if err := a.Alarms.Dismiss(ctx, stateID); err != nil {
					return fmt.Errorf("dismiss alarm: %w", err)
				}
			}

			displayID := args[0]
			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"dismissed": true, "id": displayID})
			}
			fmt.Fprintf(w, "Dismissed alarm state %s.\n", displayID)
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

The state ID is shown in the output of "alarm list" (the number in
brackets, e.g. [5] or [t5]). Only fired, non-dismissed alarms can be
snoozed. For todo alarms, use the "t" prefix (e.g. t5).

For event alarms, the snooze time is bounded by the event timeline: if
the requested duration would place the reminder after the event ends,
it is automatically capped to the event's end time. If the event has
already ended, the snooze is rejected.

Use --until-start to snooze until the moment the event begins (event
alarms only; not supported for todo alarms).

The alarm remains in the pending list (shown by "alarm list") with the
snooze-until time recorded. When "alarm check" runs after the snooze
expires, the alarm fires again. The default snooze duration is 15
minutes.

Note: snooze state is local to chroncal and is not exported to .ics files.
Exporting and re-importing a calendar will not preserve snooze times.`,
		Example: `  # Snooze for the default 15 minutes
  chroncal alarm snooze 5

  # Snooze a todo alarm for 1 hour
  chroncal alarm snooze t3 --for 1h

  # Snooze until the event starts (event alarms only)
  chroncal alarm snooze 5 --until-start

  # Snooze and get JSON output (for scripting)
  chroncal alarm snooze 5 --for 1h -o json

  # Check snooze status in the pending list
  chroncal alarm list`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			stateID, isTodo, err := parseStateID(args[0])
			if err != nil {
				return err
			}

			ctx := context.Background()
			w := cmd.OutOrStdout()
			now := time.Now()
			displayID := args[0]

			// Todo alarm snooze: simple duration-based, no event bounds.
			if isTodo {
				if untilStart {
					return errInvalidInputf("--until-start is not supported for todo alarms")
				}
				dur, err := parseCLIDuration("for", forDur)
				if err != nil {
					return err
				}
				if dur <= 0 {
					return errInvalidInputf("--for: snooze duration must be positive (e.g. 5m, 1h)")
				}
				until := now.Add(dur)
				if err := a.Alarms.SnoozeTodoAlarm(ctx, stateID, until); err != nil {
					return fmt.Errorf("snooze todo alarm: %w", err)
				}
				if outputFmt != "text" {
					return printOutput(w, map[string]any{
						"snoozed": true,
						"id":      displayID,
						"until":   until.UTC().Format(time.RFC3339),
					})
				}
				fmt.Fprintf(w, "Snoozed todo alarm state %s until %s.\n", displayID, until.Local().Format("15:04"))
				return nil
			}

			// Event alarm snooze: bounded by event timeline.
			var res alarm.SnoozeResult
			if untilStart {
				res, err = a.Alarms.SnoozeUntilStart(ctx, stateID, now)
				if err != nil {
					return fmt.Errorf("snooze until start: %w", err)
				}
			} else {
				dur, err := parseCLIDuration("for", forDur)
				if err != nil {
					return err
				}
				if dur <= 0 {
					return errInvalidInputf("--for: snooze duration must be positive (e.g. 5m, 1h)")
				}
				res, err = a.Alarms.ComputeSnooze(ctx, stateID, dur, now)
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
					"id":         displayID,
					"until":      res.Until.UTC().Format(time.RFC3339),
					"capped":     res.Capped,
					"past_start": res.PastStart,
				})
			}

			if res.Capped {
				fmt.Fprintf(os.Stderr, "chroncal: snooze capped at event end (%s)\n",
					res.EventEnd.Local().Format("15:04"))
			} else if res.PastStart {
				fmt.Fprintf(os.Stderr, "chroncal: note: alarm will fire after event starts (%s)\n",
					res.EventStart.Local().Format("15:04"))
			}
			fmt.Fprintf(w, "Snoozed alarm state %s until %s.\n", displayID, res.Until.Local().Format("15:04"))
			return nil
		},
	}
	cmd.Flags().StringVar(&forDur, "for", "15m", "snooze duration (e.g. 15m, 1h)")
	cmd.Flags().BoolVar(&untilStart, "until-start", false, "snooze until the event starts (event alarms only)")
	mutuallyExclusive(cmd, "for", "until-start")
	return cmd
}

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
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			dur, err := parseCLIDuration("interval", interval)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			w := cmd.OutOrStdout()
			fmt.Fprintf(os.Stderr, "chroncal: daemon started (interval: %s)\n", dur)
			policy := effectiveAlarmExecutionPolicy(cmd, flagPolicy)

			ticker := time.NewTicker(dur)
			defer ticker.Stop()

			// Run immediately on start, then on each tick.
			runCheck := func() {
				due, todoDue, err := a.Alarms.Check(ctx, time.Now())
				if err != nil {
					fmt.Fprintf(os.Stderr, "chroncal: check error: %v\n", err)
					return
				}
				for _, da := range due {
					if da.StateID != 0 {
						if markErr := a.Alarms.MarkRefired(ctx, da.StateID); markErr != nil {
							fmt.Fprintf(os.Stderr, "chroncal: mark-refired error: event=%q: %v\n", safeText(da.Event.Title), markErr)
						}
					} else {
						if _, markErr := a.Alarms.MarkFired(ctx, da); markErr != nil {
							fmt.Fprintf(os.Stderr, "chroncal: mark-fired error: event=%q: %v\n", safeText(da.Event.Title), markErr)
						}
					}

					fireErr := fireAlarm(da, policy)
					if fireErr != nil {
						fmt.Fprintf(os.Stderr, "chroncal: alarm error: %s (event=%q action=%s): %v\n",
							da.TriggerAt.Local().Format("15:04"), safeText(da.Event.Title), da.Alarm.Action, fireErr)
						continue
					}

					writeAlarmCheckLine(w, da.TriggerAt, da.Alarm.Action, da.Event.Title, false)
				}
				for _, tda := range todoDue {
					writeAlarmCheckLine(w, tda.TriggerAt, tda.Alarm.Action, tda.Todo.Summary, true)
				}
			}

			runCheck()

			for {
				select {
				case <-ctx.Done():
					fmt.Fprintf(os.Stderr, "chroncal: daemon stopped\n")
					return nil
				case <-ticker.C:
					runCheck()
				}
			}
		},
	}
	cmd.Flags().StringVar(&interval, "interval", "30s", "check interval (e.g. 30s, 1m)")
	bindAlarmExecutionPolicyFlags(cmd, &flagPolicy)
	return cmd
}

// todoDueAlarmToDueAlarm converts a TodoDueAlarm into a DueAlarm with a
// synthetic event populated from the todo's summary, location, and due/start
// date so that FormatNotification produces meaningful output.
func todoDueAlarmToDueAlarm(tda alarm.TodoDueAlarm) alarm.DueAlarm {
	evt := event.Event{
		Title:    tda.Todo.Summary,
		Location: tda.Todo.Location,
	}
	dateStr := tda.Todo.DueDate
	if dateStr == "" {
		dateStr = tda.Todo.StartDate
	}
	if dateStr != "" {
		if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
			evt.StartTime = t
		} else if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			evt.StartTime = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
		}
	}
	return alarm.DueAlarm{
		Alarm:     tda.Alarm,
		TriggerAt: tda.TriggerAt,
		Event:     evt,
	}
}

func alarmMissedCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "missed",
		Short: "Show alarms that were missed (older than 24h, never fired)",
		Long: `List alarms that would have fired in the lookback window but were
never acknowledged. These are alarms that were skipped because the
system was not running when they became due.`,
		Example: `  chroncal alarm missed
  chroncal alarm missed --days 3
  chroncal alarm missed --days 14 --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			now := time.Now()
			lookback := time.Duration(days) * 24 * time.Hour
			missedEvents, missedTodos, err := a.Alarms.CheckMissed(context.Background(), now, lookback)
			if err != nil {
				return fmt.Errorf("check missed: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{
					"events": missedEvents,
					"todos":  missedTodos,
				})
			}

			if len(missedEvents) == 0 && len(missedTodos) == 0 {
				fmt.Fprintln(w, "No missed alarms.")
				return nil
			}

			fmt.Fprintf(w, "Missed alarms (last %d days):\n\n", days)
			for _, m := range missedEvents {
				writeMissedAlarmLine(w, m.TriggerAt, m.EventTitle, false, m.Age)
			}
			for _, m := range missedTodos {
				writeMissedAlarmLine(w, m.TriggerAt, m.TodoSummary, true, m.Age)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "lookback window in days")
	return cmd
}
