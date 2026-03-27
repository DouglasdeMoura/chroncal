package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/ical"
)

func icalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ical",
		Short: "Import and export iCal (.ics) files",
	}
	cmd.AddCommand(icalImportCmd(), icalExportCmd())
	return cmd
}

func icalImportCmd() *cobra.Command {
	var (
		calendarName string
		jsonOut      bool
	)
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

			var imported []event.Event
			for _, e := range events {
				saved, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
					UID:            e.UID,
					CalendarID:     calID,
					Title:          e.Title,
					Description:    e.Description,
					Location:       e.Location,
					StartTime:      e.StartTime,
					EndTime:        e.EndTime,
					AllDay:         e.AllDay,
					RecurrenceRule: e.RecurrenceRule,
					Timezone:       e.Timezone,
					Status:         e.Status,
					Transp:         e.Transp,
					Sequence:       e.Sequence,
					Priority:       e.Priority,
					Class:          e.Class,
					URL:            e.URL,
					Categories:     e.Categories,
					ExDates:        e.ExDates,
					RDates:         e.RDates,
					RecurrenceID:   e.RecurrenceID,
				})
				if err != nil {
					return fmt.Errorf("upsert event %q: %w", e.Title, err)
				}

				if len(e.Alarms) > 0 {
					if err := a.Events.ReplaceAlarms(ctx, saved.ID, e.Alarms); err != nil {
						return fmt.Errorf("replace alarms for %q: %w", e.Title, err)
					}
				}
				if len(e.Attendees) > 0 {
					if err := a.Events.ReplaceAttendees(ctx, saved.ID, e.Attendees); err != nil {
						return fmt.Errorf("replace attendees for %q: %w", e.Title, err)
					}
				}

				imported = append(imported, saved)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				items := make([]jsonEvent, len(imported))
				for i, e := range imported {
					items[i] = toJSONEvent(e)
				}
				return printJSON(w, items)
			}
			fmt.Fprintf(w, "Imported %d events.\n", len(imported))
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "Personal", "calendar to import into")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output imported events as JSON")
	return cmd
}

func icalExportCmd() *cobra.Command {
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

			// Load alarms and attendees for each event
			for i := range events {
				events[i].Alarms, _ = a.Events.ListAlarms(ctx, events[i].ID)
				events[i].Attendees, _ = a.Events.ListAttendees(ctx, events[i].ID)
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
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 14 days from now)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file (default: stdout)")
	return cmd
}
