package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/app"
	"github.com/douglasdemoura/tcal/internal/tui"
)

var dbPath string

var rootCmd = &cobra.Command{
	Use:   "tcal",
	Short: "A beautiful terminal calendar",
	Long: `tcal is a terminal calendar backed by SQLite with iCal import/export.

Launch the TUI by running tcal with no arguments, or use subcommands
for scriptable access to all calendar operations.

Resource groups:
  event      Manage events (list, get, add, update, delete)
  calendar   Manage calendars (list, get, create, update, delete)
  ical       Import and export iCal (.ics) files`,
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
	path := dbPath
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
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "path to SQLite database (default: $XDG_CONFIG_HOME/tcal/tcal.db)")

	// Primary subcommand groups
	rootCmd.AddCommand(eventCmd(), calendarCmd(), icalCmd())

	// Backward-compatible aliases (hidden)
	rootCmd.AddCommand(aliasCmd("add", "event add", eventAddCmd))
	rootCmd.AddCommand(aliasCmd("list", "event list", eventListCmd))
	rootCmd.AddCommand(aliasCmd("import", "ical import", icalImportCmd))
	rootCmd.AddCommand(aliasCmd("export", "ical export", icalExportCmd))
}

func aliasCmd(name, target string, factory func() *cobra.Command) *cobra.Command {
	cmd := factory()
	cmd.Use = strings.Replace(cmd.Use, strings.Fields(cmd.Use)[0], name, 1)
	cmd.Short = fmt.Sprintf("Alias for '%s'", target)
	cmd.Hidden = true
	cmd.Deprecated = fmt.Sprintf("use 'tcal %s' instead", target)
	return cmd
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
