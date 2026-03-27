package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/app"
	"github.com/douglasdemoura/tcal/internal/config"
	"github.com/douglasdemoura/tcal/internal/tui"
)

var (
	jsonOut bool
	cfg     config.Config
)

var rootCmd = &cobra.Command{
	Use:   "tcal",
	Short: "A beautiful terminal calendar",
	Long: `tcal is a terminal calendar backed by SQLite with iCal import/export.

Launch the TUI by running tcal with no arguments, or use subcommands
for scriptable access to all calendar operations.

Resource groups:
  event      Manage events (list, get, add, update, delete)
  todo       Manage todos (list, get, add, update, delete)
  calendar   Manage calendars (list, get, create, update, delete)
  ical       Import and export iCal (.ics) files`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cfg = config.Load()
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
	// Precedence: TCAL_DB env > config.toml > default
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
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "output as JSON")

	rootCmd.AddCommand(eventCmd(), calendarCmd(), todoCmd(), icalCmd())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func resolveCalendarID(ctx context.Context, a *app.App, name string) (int64, error) {
	cals, err := a.Calendars.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list calendars: %w", err)
	}
	for _, c := range cals {
		if strings.EqualFold(c.Name, name) {
			return c.ID, nil
		}
	}
	return 0, fmt.Errorf("calendar %q not found", name)
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
	}
	return from, to, nil
}
