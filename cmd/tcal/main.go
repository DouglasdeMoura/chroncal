package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/app"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/ical"
	"github.com/douglasdemoura/tcal/internal/tui"
)

var dbPath string

var rootCmd = &cobra.Command{
	Use:   "tcal",
	Short: "A beautiful terminal calendar",
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
	rootCmd.AddCommand(importCmd(), exportCmd(), addCmd(), listCmd())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func importCmd() *cobra.Command {
	var calendarName string
	cmd := &cobra.Command{
		Use:   "import <file.ics>",
		Short: "Import events from an iCal (.ics) file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open file: %w", err)
			}
			defer f.Close()

			events, err := ical.ImportFile(f)
			if err != nil {
				return fmt.Errorf("import: %w", err)
			}

			calID, err := resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}

			for _, e := range events {
				_, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
					UID:            e.UID,
					CalendarID:     calID,
					Title:          e.Title,
					Description:    e.Description,
					Location:       e.Location,
					StartTime:      e.StartTime,
					EndTime:        e.EndTime,
					AllDay:         e.AllDay,
					RecurrenceRule: e.RecurrenceRule,
				})
				if err != nil {
					return fmt.Errorf("upsert event %q: %w", e.Title, err)
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Imported %d events.\n", len(events))
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "Personal", "calendar to import into")
	return cmd
}

func exportCmd() *cobra.Command {
	var (
		calendarName string
		fromStr      string
		toStr        string
		output       string
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export events to iCal (.ics) format",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			from, to, err := parseDateRange(fromStr, toStr)
			if err != nil {
				return err
			}

			var events []event.Event
			if calendarName != "" {
				calID, err := resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
				events, err = a.Events.ListByCalendarAndDateRange(ctx, calID, from, to)
				if err != nil {
					return fmt.Errorf("list events: %w", err)
				}
			} else {
				events, err = a.Events.ListByDateRange(ctx, from, to)
				if err != nil {
					return fmt.Errorf("list events: %w", err)
				}
			}

			data, err := ical.ExportEvents(events, calendarName)
			if err != nil {
				return err
			}

			if output != "" {
				if err := os.WriteFile(output, data, 0o644); err != nil {
					return fmt.Errorf("write file: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Exported %d events to %s\n", len(events), output)
			} else {
				cmd.OutOrStdout().Write(data)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "export only this calendar")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: 1 year ago)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 1 year from now)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file (default: stdout)")
	return cmd
}

func addCmd() *cobra.Command {
	var (
		dateStr      string
		timeStr      string
		durationStr  string
		calendarName string
		location     string
	)
	cmd := &cobra.Command{
		Use:   `add "<title>"`,
		Short: "Quick-add an event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			calID, err := resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}

			now := time.Now()
			date := now
			if dateStr != "" {
				date, err = time.ParseInLocation("2006-01-02", dateStr, time.Local)
				if err != nil {
					return fmt.Errorf("parse date: %w", err)
				}
			}

			allDay := timeStr == ""
			startTime := time.Date(date.Year(), date.Month(), date.Day(), 9, 0, 0, 0, time.Local)
			if timeStr != "" {
				t, err := time.Parse("15:04", timeStr)
				if err != nil {
					return fmt.Errorf("parse time: %w", err)
				}
				startTime = time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
			}

			dur := time.Hour
			if durationStr != "" {
				dur, err = time.ParseDuration(durationStr)
				if err != nil {
					return fmt.Errorf("parse duration: %w", err)
				}
			}

			endTime := startTime.Add(dur)
			if allDay {
				startTime = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
				endTime = startTime.AddDate(0, 0, 1)
			}

			e, err := a.Events.Create(ctx, event.CreateParams{
				CalendarID: calID,
				Title:      args[0],
				Location:   location,
				StartTime:  startTime,
				EndTime:    endTime,
				AllDay:     allDay,
			})
			if err != nil {
				return fmt.Errorf("create event: %w", err)
			}

			if allDay {
				fmt.Fprintf(cmd.OutOrStdout(), "Created: %s on %s (all day)\n", e.Title, e.StartTime.Format("Mon, Jan 2 2006"))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Created: %s on %s at %s (%s)\n", e.Title, e.StartTime.Format("Mon, Jan 2 2006"), e.StartTime.Format("15:04"), dur)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dateStr, "date", "", "event date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&timeStr, "time", "", "start time (HH:MM, default: all-day)")
	cmd.Flags().StringVar(&durationStr, "duration", "1h", "event duration (e.g. 30m, 1h30m)")
	cmd.Flags().StringVar(&calendarName, "calendar", "Personal", "calendar name")
	cmd.Flags().StringVar(&location, "location", "", "event location")
	return cmd
}

func listCmd() *cobra.Command {
	var (
		fromStr      string
		toStr        string
		calendarName string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List upcoming events",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			from, to, err := parseDateRange(fromStr, toStr)
			if err != nil {
				return err
			}

			var events []event.Event
			if calendarName != "" {
				calID, err := resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
				events, err = a.Events.ListByCalendarAndDateRange(ctx, calID, from, to)
				if err != nil {
					return fmt.Errorf("list events: %w", err)
				}
			} else {
				events, err = a.Events.ListByDateRange(ctx, from, to)
				if err != nil {
					return fmt.Errorf("list events: %w", err)
				}
			}

			if len(events) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No events found.")
				return nil
			}

			var currentDate string
			for _, e := range events {
				dateLabel := e.StartTime.Format("Mon, Jan 2 2006")
				if dateLabel != currentDate {
					if currentDate != "" {
						fmt.Fprintln(cmd.OutOrStdout())
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", dateLabel)
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", strings.Repeat("─", len(dateLabel)))
					currentDate = dateLabel
				}
				if e.AllDay {
					fmt.Fprintf(cmd.OutOrStdout(), "  ● all day   %s\n", e.Title)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "  ● %s  %s\n", e.StartTime.Format("15:04"), e.Title)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 14 days from now)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	return cmd
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
