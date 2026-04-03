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
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/douglasdemoura/chroncal/internal/tui"
)

// notFoundErr wraps sql.ErrNoRows into a user-friendly message.
func notFoundErr(err error, resource string, id any) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s %v not found", resource, id)
	}
	return err
}

var (
	outputFmt string
	cfg       config.Config
)

var rootCmd = &cobra.Command{
	Use:   "chroncal",
	Short: "A beautiful terminal calendar",
	Long: `chroncal is a terminal calendar backed by SQLite with iCal import/export.

Launch the TUI by running chroncal with no arguments, or use subcommands
for scriptable access to all calendar operations.

Resource groups:
  event      Manage events (list, get, add, update, delete)
  todo       Manage todos (list, get, add, update, delete)
  journal    Manage journal entries (list, get, add, update, delete)
  calendar   Manage calendars (list, get, create, update, delete)
  ical       Import and export iCal (.ics) files
  alarm      Manage alarm notifications (check, list, dismiss, snooze, daemon)
  tick       Run one service tick (alarms always, sync when due)
  service    Manage alarm notification service (install, uninstall, status)`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cfg = config.Load()
		if cfg.ProductID != "" {
			ical.ProductID = cfg.ProductID
		}
		switch outputFmt {
		case "text", "table", "json", "yaml":
			return nil
		default:
			return fmt.Errorf("invalid output format %q (must be text, table, json, or yaml)", outputFmt)
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := initApp()
		if err != nil {
			return err
		}
		defer a.Close()
		return tui.Run(a)
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
	return app.New(path)
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "text", "output format (text, table, json, yaml)")

	rootCmd.AddCommand(eventCmd(), calendarCmd(), todoCmd(), journalCmd(), icalCmd(), alarmCmd(), tickCmd(), serviceCmd(), accountCmd(), syncCmd())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// resolveEvent looks up an event by numeric ID, string UID, or UID + recurrence-id.
func resolveEvent(ctx context.Context, a *app.App, ref, recurrenceID string) (event.Event, error) {
	var e event.Event
	var err error
	if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		e, err = a.Events.Get(ctx, id)
	} else if recurrenceID != "" {
		e, err = a.Events.GetByUIDAndRecurrenceID(ctx, ref, recurrenceID)
	} else {
		e, err = a.Events.GetByUID(ctx, ref)
	}
	if err != nil {
		return e, notFoundErr(err, "event", ref)
	}
	return e, nil
}

// resolveTodo looks up a todo by numeric ID, string UID, or UID + recurrence-id.
func resolveTodo(ctx context.Context, a *app.App, ref, recurrenceID string) (todo.Todo, error) {
	var t todo.Todo
	var err error
	if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		t, err = a.Todos.Get(ctx, id)
	} else if recurrenceID != "" {
		t, err = a.Todos.GetByUIDAndRecurrenceID(ctx, ref, recurrenceID)
	} else {
		t, err = a.Todos.GetByUID(ctx, ref)
	}
	if err != nil {
		return t, notFoundErr(err, "todo", ref)
	}
	return t, nil
}

func resolveCalendarID(ctx context.Context, a *app.App, name string) (int64, error) {
	cals, err := a.Calendars.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list calendars: %w", err)
	}
	if name == "" {
		// No calendar specified: use the first available calendar.
		if len(cals) == 0 {
			return 0, fmt.Errorf("no calendars exist")
		}
		return cals[0].ID, nil
	}
	for _, c := range cals {
		if strings.EqualFold(c.Name, name) {
			return c.ID, nil
		}
	}
	return 0, fmt.Errorf("calendar %q not found", name)
}

// resolveJournal looks up a journal by numeric ID, string UID, or UID + recurrence-id.
func resolveJournal(ctx context.Context, a *app.App, ref, recurrenceID string) (journal.Journal, error) {
	var j journal.Journal
	var err error
	if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		j, err = a.Journals.Get(ctx, id)
	} else if recurrenceID != "" {
		j, err = a.Journals.GetByUIDAndRecurrenceID(ctx, ref, recurrenceID)
	} else {
		j, err = a.Journals.GetByUID(ctx, ref)
	}
	if err != nil {
		return j, notFoundErr(err, "journal", ref)
	}
	return j, nil
}

func parseDateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 0, 14)

	if fromStr != "" {
		var err error
		from, err = time.ParseInLocation("2006-01-02", fromStr, time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parse --from: %w", err)
		}
	}
	if toStr != "" {
		var err error
		to, err = time.ParseInLocation("2006-01-02", toStr, time.Local)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parse --to: %w", err)
		}
		to = to.AddDate(0, 0, 1) // half-open: include the entire end day
	}
	return from, to, nil
}
