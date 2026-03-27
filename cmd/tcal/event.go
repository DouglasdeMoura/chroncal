package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/event"
)

func eventCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "event",
		Short: "Manage events",
	}
	cmd.AddCommand(eventListCmd(), eventGetCmd(), eventAddCmd(), eventUpdateCmd(), eventDeleteCmd())
	return cmd
}

func eventListCmd() *cobra.Command {
	var (
		fromStr      string
		toStr        string
		calendarName string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List events in a date range",
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

			w := cmd.OutOrStdout()
			if jsonOut {
				items := make([]jsonEvent, len(events))
				for i, e := range events {
					items[i] = toJSONEvent(e)
				}
				return printJSON(w, items)
			}
			printEvents(w, events)
			return nil
		},
	}
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 14 days from now)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	return cmd
}

func eventGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get event details by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid event ID: %w", err)
			}

			e, err := a.Events.Get(ctx, id)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}

			e.Alarms, _ = a.Events.ListAlarms(ctx, e.ID)
			e.Attendees, _ = a.Events.ListAttendees(ctx, e.ID)

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, toJSONEvent(e))
			}
			printEvent(w, e)
			return nil
		},
	}
	return cmd
}

func eventAddCmd() *cobra.Command {
	var (
		dateStr      string
		timeStr      string
		durationStr  string
		calendarName string
		location     string
		description  string
		status       string
		url          string
		categories   string
		class        string
		transp       string
		priority     int64
	)
	cmd := &cobra.Command{
		Use:   `add "<title>"`,
		Short: "Create a new event",
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
				CalendarID:  calID,
				Title:       args[0],
				Description: description,
				Location:    location,
				StartTime:   startTime,
				EndTime:     endTime,
				AllDay:      allDay,
				Status:      status,
				URL:         url,
				Categories:  categories,
				Class:       class,
				Transp:      transp,
				Priority:    priority,
			})
			if err != nil {
				return fmt.Errorf("create event: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, toJSONEvent(e))
			}
			if allDay {
				fmt.Fprintf(w, "Created: %s on %s (all day)\n", e.Title, e.StartTime.Local().Format("Mon, Jan 2 2006"))
			} else {
				fmt.Fprintf(w, "Created: %s on %s at %s (%s)\n", e.Title, e.StartTime.Local().Format("Mon, Jan 2 2006"), e.StartTime.Local().Format("15:04"), dur)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dateStr, "date", "", "event date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&timeStr, "time", "", "start time (HH:MM, default: all-day)")
	cmd.Flags().StringVar(&durationStr, "duration", "1h", "event duration (e.g. 30m, 1h30m)")
	cmd.Flags().StringVar(&calendarName, "calendar", "Personal", "calendar name")
	cmd.Flags().StringVar(&location, "location", "", "event location")
	cmd.Flags().StringVar(&description, "description", "", "event description")
	cmd.Flags().StringVar(&status, "status", "", "event status (TENTATIVE, CONFIRMED, CANCELLED)")
	cmd.Flags().StringVar(&url, "url", "", "associated URL")
	cmd.Flags().StringVar(&categories, "categories", "", "comma-separated categories")
	cmd.Flags().StringVar(&class, "class", "", "classification (PUBLIC, PRIVATE, CONFIDENTIAL)")
	cmd.Flags().StringVar(&transp, "transp", "", "transparency (OPAQUE, TRANSPARENT)")
	cmd.Flags().Int64Var(&priority, "priority", 0, "priority (0-9)")
	return cmd
}

func eventUpdateCmd() *cobra.Command {
	var (
		title        string
		dateStr      string
		timeStr      string
		durationStr  string
		calendarName string
		location     string
		description  string
		status       string
		url          string
		categories   string
		class        string
		transp       string
		priority     int64
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update an existing event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid event ID: %w", err)
			}

			existing, err := a.Events.Get(ctx, id)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}

			p := event.UpdateParams{
				Title:          existing.Title,
				Description:    existing.Description,
				Location:       existing.Location,
				StartTime:      existing.StartTime,
				EndTime:        existing.EndTime,
				AllDay:         existing.AllDay,
				RecurrenceRule: existing.RecurrenceRule,
				CalendarID:     existing.CalendarID,
				Timezone:       existing.Timezone,
				Status:         existing.Status,
				Transp:         existing.Transp,
				Priority:       existing.Priority,
				Class:          existing.Class,
				URL:            existing.URL,
				Categories:     existing.Categories,
				ExDates:        existing.ExDates,
				RDates:         existing.RDates,
			}

			if cmd.Flags().Changed("title") {
				p.Title = title
			}
			if cmd.Flags().Changed("description") {
				p.Description = description
			}
			if cmd.Flags().Changed("location") {
				p.Location = location
			}
			if cmd.Flags().Changed("calendar") {
				calID, err := resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
				p.CalendarID = calID
			}
			if cmd.Flags().Changed("status") {
				p.Status = status
			}
			if cmd.Flags().Changed("url") {
				p.URL = url
			}
			if cmd.Flags().Changed("categories") {
				p.Categories = categories
			}
			if cmd.Flags().Changed("class") {
				p.Class = class
			}
			if cmd.Flags().Changed("transp") {
				p.Transp = transp
			}
			if cmd.Flags().Changed("priority") {
				p.Priority = priority
			}

			if cmd.Flags().Changed("date") || cmd.Flags().Changed("time") {
				date := p.StartTime.Local()
				if cmd.Flags().Changed("date") {
					d, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
					if err != nil {
						return fmt.Errorf("parse date: %w", err)
					}
					date = time.Date(d.Year(), d.Month(), d.Day(), date.Hour(), date.Minute(), 0, 0, time.Local)
				}
				if cmd.Flags().Changed("time") {
					t, err := time.Parse("15:04", timeStr)
					if err != nil {
						return fmt.Errorf("parse time: %w", err)
					}
					date = time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
					p.AllDay = false
				}
				p.StartTime = date
			}

			if cmd.Flags().Changed("duration") {
				dur, err := time.ParseDuration(durationStr)
				if err != nil {
					return fmt.Errorf("parse duration: %w", err)
				}
				p.EndTime = p.StartTime.Add(dur)
			} else if cmd.Flags().Changed("date") || cmd.Flags().Changed("time") {
				p.EndTime = p.StartTime.Add(existing.EndTime.Sub(existing.StartTime))
			}

			e, err := a.Events.Update(ctx, id, p)
			if err != nil {
				return fmt.Errorf("update event: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, toJSONEvent(e))
			}
			printEvent(w, e)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&dateStr, "date", "", "new date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&timeStr, "time", "", "new start time (HH:MM)")
	cmd.Flags().StringVar(&durationStr, "duration", "", "new duration (e.g. 30m, 1h30m)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "move to calendar (by name)")
	cmd.Flags().StringVar(&location, "location", "", "new location")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&status, "status", "", "new status (TENTATIVE, CONFIRMED, CANCELLED)")
	cmd.Flags().StringVar(&url, "url", "", "new URL")
	cmd.Flags().StringVar(&categories, "categories", "", "new categories (comma-separated)")
	cmd.Flags().StringVar(&class, "class", "", "new classification (PUBLIC, PRIVATE, CONFIDENTIAL)")
	cmd.Flags().StringVar(&transp, "transp", "", "new transparency (OPAQUE, TRANSPARENT)")
	cmd.Flags().Int64Var(&priority, "priority", 0, "new priority (0-9)")
	return cmd
}

func eventDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid event ID: %w", err)
			}

			if err := a.Events.Delete(context.Background(), id); err != nil {
				return fmt.Errorf("delete event: %w", err)
			}

			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, map[string]any{"deleted": true, "id": id})
			}
			fmt.Fprintf(w, "Deleted event %d.\n", id)
			return nil
		},
	}
	return cmd
}
