package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/tui"
)

func eventCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "event",
		Short: "Manage events",
		Long: `Create, search, inspect, update, and delete calendar events.

Events may be one-time or recurring, all-day or timed, and can include
alarms, attendees, attachments, and other iCalendar metadata.`,
		Example: `  chroncal event list
  chroncal event add "Demo" --date 2026-04-10 --time 14:00 --duration 1h
  chroncal event get 42`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		eventListCmd(), eventGetCmd(), eventAddCmd(), eventUpdateCmd(),
		eventDeleteCmd(), eventSearchCmd(),
		eventRestoreCmd(), eventPurgeCmd(), eventPurgeDeletedCmd(),
	)
	return cmd
}

func eventListCmd() *cobra.Command {
	var (
		fromStr        string
		toStr          string
		calendarName   string
		status         string
		showWeekday    bool
		verbose        bool
		showID         bool
		showCalendar   bool
		includeDeleted bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List events in a date range",
		Long: `List events in a date range, expanding recurring series into the
instances that fall inside the requested window.

Without flags, the window defaults to today through the next 30 days.`,
		Example: `  chroncal event list
  chroncal event list --calendar Work --from 2026-04-01 --to 2026-04-07
  chroncal event list --status CONFIRMED --output json`,
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

			var calID int64
			if calendarName != "" {
				calID, err = resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
			}

			events, err := a.Recurrences.ListFilteredEvents(ctx, recurrence.EventListParams{
				CalendarID:     calID,
				Status:         status,
				From:           from,
				To:             to,
				IncludeDeleted: includeDeleted,
			})
			if err != nil {
				return fmt.Errorf("list events: %w", err)
			}

			var calendarNames map[int64]string
			if verbose || showCalendar {
				cals, err := a.Calendars.List(ctx)
				if err != nil {
					return fmt.Errorf("list calendars: %w", err)
				}
				calendarNames = make(map[int64]string, len(cals))
				for _, cal := range cals {
					calendarNames[cal.ID] = cal.Name
				}
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				items := make([]jsonEvent, len(events))
				for i, e := range events {
					items[i] = toJSONEvent(e)
				}
				return printOutput(w, items)
			}
			if len(events) == 0 {
				fmt.Fprintln(w, "No events found.")
				return nil
			}
			// ShowAllDays:false suppresses date-only stub lines for days
			// with no events. Days with events still render normally.
			fmt.Fprint(w, tui.FormatEventList(tui.FormatEventListOptions{
				Events:        events,
				CalendarNames: calendarNames,
				ShowHeader:    false,
				ShowAllDays:   false,
				ShowWeekday:   showWeekday,
				ShowMonth:     true,
				Verbose:       verbose,
				ShowID:        showID,
				ShowCalendar:  showCalendar,
				From:          from,
				To:            to,
			}))
			return nil
		},
	}
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 30 days from now)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (TENTATIVE, CONFIRMED, CANCELLED)")
	cmd.Flags().BoolVar(&showWeekday, "show-weekday", false, "show weekday abbreviation next to the date")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "render a detailed time-rail view for each event")
	cmd.Flags().BoolVar(&showID, "show-id", false, "show each event's numeric ID in text output")
	cmd.Flags().BoolVar(&showCalendar, "show-calendar", false, "show the calendar name in text output")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted events (see `events restore`)")
	return cmd
}

func eventRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <id-or-uid>",
		Short: "Restore a soft-deleted event",
		Long: `Restore clears the deletion marker on a soft-deleted event so it
reappears in list and TUI views.

The event must have been deleted via chroncal (soft-delete, not purged).
Use 'events list --include-deleted' to see deletable candidates.

If the event was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely (with a fresh resource URL).`,
		Example: `  chroncal event restore 42
  chroncal event restore my-event-uid`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			ref := args[0]
			w := cmd.OutOrStdout()

			if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
				if err := a.Events.RestoreByID(ctx, id); err != nil {
					if errors.Is(err, event.ErrNotDeleted) {
						return fmt.Errorf("event %d not found (may have been purged)", id)
					}
					return fmt.Errorf("restore event: %w", err)
				}
				if outputFmt != "text" {
					return printOutput(w, map[string]any{"restored": true, "id": id})
				}
				fmt.Fprintf(w, "Restored event %d.\n", id)
				return nil
			}

			// UID path: restore every row sharing the UID.
			if err := a.Events.RestoreByUID(ctx, ref); err != nil {
				return fmt.Errorf("restore event: %w", err)
			}
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"restored": true, "uid": ref})
			}
			fmt.Fprintf(w, "Restored event(s) with uid %q.\n", safeText(ref))
			return nil
		},
	}
	return cmd
}

func eventPurgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "purge <id>",
		Short: "Hard-delete a single soft-deleted event",
		Long: `Purge permanently removes one soft-deleted event from the database.

The event must already be soft-deleted. Purging a live event is refused;
use 'event delete' first. Purging is not reversible — child rows (alarms,
attendees, attachments, overrides) cascade.`,
		Example: `  chroncal event purge 42
  chroncal event purge 42 --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return errInvalidInputf("parse id %q: %v", args[0], err)
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			e, err := a.Events.GetIncludingDeleted(ctx, id)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}
			if e.DeletedAt == nil {
				return fmt.Errorf("event %d is live; run 'event delete %d' first", id, id)
			}

			question := fmt.Sprintf("Purge event %q (id %d)? This cannot be undone.", safeText(e.Title), id)
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if err := a.Events.PurgeByID(ctx, id); err != nil {
				if errors.Is(err, event.ErrNotDeleted) {
					return fmt.Errorf("event %d not found or not soft-deleted", id)
				}
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": true, "id": id})
			}
			fmt.Fprintf(w, "Purged event %d.\n", id)
			return nil
		},
	}
	addConfirmFlag(cmd)
	return cmd
}

func eventPurgeDeletedCmd() *cobra.Command {
	var olderThanStr string
	cmd := &cobra.Command{
		Use:   "purge-deleted",
		Short: "Hard-delete soft-deleted events older than --older-than",
		Long: `Purge permanently removes soft-deleted events from the database.

By default, only events soft-deleted more than 30 days ago are purged.
Use --older-than to pick a different age (e.g. 7d, 24h, 720h).

This operation is destructive and not reversible. Attachments and other
child rows cascade. Soft-delete protection is bypassed for anything
matching the age threshold.`,
		Example: `  chroncal event purge-deleted                   # 30 days by default
  chroncal event purge-deleted --older-than 7d   # older than a week
  chroncal event purge-deleted --older-than 0s --yes  # purge everything soft-deleted`,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := time.ParseDuration(olderThanStr)
			if err != nil {
				return errInvalidInputf("parse --older-than %q: %v", olderThanStr, err)
			}
			if d < 0 {
				return errInvalidInputf("--older-than must be non-negative, got %s", d)
			}

			// Sub-hour windows are especially destructive — require --yes
			// or an interactive confirm regardless of scripted-vs-tty.
			if d < time.Hour {
				prompt := fmt.Sprintf("Purge ALL events soft-deleted in the last %s? This cannot be undone.", d)
				if err := confirmDestructive(cmd, prompt); err != nil {
					return err
				}
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			cutoff := time.Now().Add(-d)
			n, err := a.Events.PurgeDeleted(ctx, cutoff)
			if err != nil {
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": n, "older_than": d.String()})
			}
			fmt.Fprintf(w, "Purged %d event(s) soft-deleted more than %s ago.\n", n, d)
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThanStr, "older-than", "720h", "age threshold (Go duration, e.g. 30d=720h, 168h=7 days)")
	addConfirmFlag(cmd)
	return cmd
}

func eventSearchCmd() *cobra.Command {
	var (
		calendarName string
		fromStr      string
		toStr        string
		status       string
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search events by title, description, location, or categories",
		Long: `Search events by text fields such as title, description, location,
and categories.

Use --from and --to to narrow the search window when you already know
roughly when the event occurred.`,
		Example: `  chroncal event search standup
  chroncal event search deploy --calendar Work --status CONFIRMED
  chroncal event search conference --from 2026-04-01T00:00:00Z --to 2026-05-01T00:00:00Z`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			var calID int64
			if calendarName != "" {
				calID, err = resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
			}

			events, err := a.Events.Search(ctx, event.SearchParams{
				Query:      args[0],
				CalendarID: calID,
				From:       fromStr,
				To:         toStr,
				Status:     status,
			})
			if err != nil {
				return fmt.Errorf("search events: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				items := make([]jsonEvent, len(events))
				for i, e := range events {
					items[i] = toJSONEvent(e)
				}
				return printOutput(w, items)
			}
			if len(events) == 0 {
				fmt.Fprintln(w, "No events found.")
				return nil
			}
			fmt.Fprint(w, tui.FormatEventList(tui.FormatEventListOptions{
				Events:      events,
				ShowAllDays: false,
				ShowMonth:   true,
			}))
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date filter (RFC3339)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date filter (RFC3339)")
	cmd.Flags().StringVar(&status, "status", "", "status filter (TENTATIVE, CONFIRMED, CANCELLED)")
	return cmd
}

func eventGetCmd() *cobra.Command {
	var recurrenceID string
	cmd := &cobra.Command{
		Use:   "get <id|uid>",
		Short: "Get event details by ID or UID",
		Long: `Show one event in detail.

You can look up the event by numeric ID or by its UID. Use
--recurrence-id to target a specific overridden instance from a
recurring series.`,
		Example: `  chroncal event get 42
  chroncal event get 6d7d8c3b-uid
  chroncal event get team-standup-uid --recurrence-id 2026-04-06T12:00:00Z --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			e, err := resolveEvent(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}

			populateEventFields(ctx, a.Events, &e)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONEvent(e))
			}
			printEvent(w, e)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}

func eventAddCmd() *cobra.Command {
	var (
		dateStr       string
		timeStr       string
		endDateStr    string
		endTimeStr    string
		durationStr   string
		calendarName  string
		location      string
		description   string
		status        string
		url           string
		categories    string
		class         string
		transp        string
		priority      int64
		rrule         string
		timezone      string
		geo           string
		exdates       []string
		rdates        []string
		attachFlags   []string
		alarmFlags    []string
		attendeeFlags []string
		commentFlags  []string
		contactFlags  []string
		resourceFlags []string
		relationFlags []string
		organizer     string
	)
	cmd := &cobra.Command{
		Use:   `add "<title>"`,
		Short: "Create a new event",
		Long: `Create a new event in the calendar.

Omitting --time creates an all-day event. When --time is set, the event
defaults to 1 hour unless --duration or --end-time is provided.

Use --end-date to span multiple days. For all-day events --end-date is the
last day inclusive. For timed events --end-date must be paired with
--end-time to set the exact end moment.

Defaults: status=CONFIRMED, class=PUBLIC, transparency=OPAQUE, calendar=Personal.
Attendees default to RSVP=NEEDS-ACTION and ROLE=REQ-PARTICIPANT.
Alarms default to ACTION=DISPLAY unless prefixed (e.g. EMAIL:-PT1H).`,
		Example: `  # Timed event tomorrow at 2pm for 90 minutes
  chroncal event add "Lunch with Alice" --date 2026-04-01 --time 14:00 --duration 1h30m

  # All-day event (no --time flag)
  chroncal event add "Company Holiday" --date 2026-12-25

  # Multi-day all-day event (--end-date is inclusive)
  chroncal event add "Summer vacation" --date 2026-07-05 --end-date 2026-07-15

  # Event with explicit end time instead of duration
  chroncal event add "Workshop" --date 2026-05-10 --time 09:00 --end-time 12:30

  # Timed event that crosses midnight
  chroncal event add "Overnight hackathon" --date 2026-06-13 --time 18:00 \
    --end-date 2026-06-14 --end-time 12:00

  # Recurring weekly meeting with alarm and attendees
  chroncal event add "Team Standup" --time 09:00 --duration 30m \
    --rrule "FREQ=WEEKLY;BYDAY=MO,WE,FR" \
    --alarm "-PT15M" --attendee "Alice <alice@example.com>"

  # Event with timezone, location, and categories
  chroncal event add "Conference Talk" --date 2026-05-10 --time 10:00 \
    --timezone America/New_York --location "Room 42" --categories "work,conference"

  # Recurring event with an excluded date
  chroncal event add "Sprint Review" --time 14:00 \
    --rrule "FREQ=WEEKLY;COUNT=10" --exdate 2026-04-08

  # High-priority event with comment and file attachment
  chroncal event add "Board Meeting" --date 2026-06-01 --time 10:00 \
    --priority 1 --comment "Bring Q2 financials" \
    --attach /path/to/agenda.pdf

  # Multiple alarm types: display (default), email, and audio
  chroncal event add "Deploy Window" --date 2026-04-15 --time 02:00 \
    --alarm "-PT1H" --alarm "EMAIL:-P1D" --alarm "AUDIO:-PT5M"

  # Alarm that repeats 3 times every 5 minutes, relative to event end
  chroncal event add "Deadline" --date 2026-04-15 --time 17:00 \
    --alarm "DISPLAY:-PT30M::3:PT5M:END"

  # EMAIL alarm with attendees
  chroncal event add "Team Sync" --date 2026-04-15 --time 09:00 \
    --alarm "EMAIL:-PT1H:::::alice@example.com,bob@example.com"

  # Event with organizer, contacts, and resources
  chroncal event add "Board Meeting" --date 2026-06-01 --time 10:00 \
    --organizer "Alice <alice@example.com>" \
    --contact "Bob Smith, 555-1234" --resource PROJECTOR --resource WHITEBOARD

  # Link events with RELATED-TO (parent/child/sibling)
  chroncal event add "Sprint Planning" --time 14:00 \
    --related-to "PARENT:quarterly-review-uid"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			if strings.TrimSpace(args[0]) == "" {
				return errInvalidInputf("event title must not be empty")
			}

			calID, err := resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}

			if err := validateEventEnums(status, class, transp, priority); err != nil {
				return err
			}
			if err := validateRRule(rrule); err != nil {
				return err
			}
			if err := validateGeo(geo); err != nil {
				return err
			}
			if err := validateURL(url); err != nil {
				return err
			}

			now := time.Now()
			loc := time.Local
			if timezone != "" {
				loc, err = time.LoadLocation(timezone)
				if err != nil {
					return fmt.Errorf("load timezone: %w", err)
				}
			}

			date := now.In(loc)
			if dateStr != "" {
				date, err = parseCLIDate("date", dateStr, loc)
				if err != nil {
					return err
				}
			}

			allDay := timeStr == ""
			startTime := time.Date(date.Year(), date.Month(), date.Day(), 9, 0, 0, 0, loc)
			if timeStr != "" {
				t, err := parseCLITime("time", timeStr)
				if err != nil {
					return err
				}
				startTime = time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			}

			if endTimeStr != "" && cmd.Flags().Changed("duration") {
				return errInvalidInputf("--end-time and --duration are mutually exclusive")
			}
			if endDateStr != "" && cmd.Flags().Changed("duration") {
				return errInvalidInputf("--end-date and --duration are mutually exclusive")
			}

			var endDate time.Time
			if endDateStr != "" {
				endDate, err = parseCLIDate("end-date", endDateStr, loc)
				if err != nil {
					return err
				}
			}

			var endTime time.Time
			switch {
			case allDay:
				startTime = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
				if endDateStr != "" {
					if endDate.Before(date) {
						return errInvalidInputf("--end-date %s is before --date %s", endDateStr, date.Format("2006-01-02"))
					}
					endTime = time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
				} else {
					endTime = startTime.AddDate(0, 0, 1)
				}
			case endTimeStr != "":
				t, err := parseCLITime("end-time", endTimeStr)
				if err != nil {
					return err
				}
				endRef := date
				if endDateStr != "" {
					endRef = endDate
				}
				endTime = time.Date(endRef.Year(), endRef.Month(), endRef.Day(), t.Hour(), t.Minute(), 0, 0, loc)
				if !endTime.After(startTime) {
					return errInvalidInputf("end %s is not after start %s (use --end-date to cross midnight, or --duration)",
						endTime.Format("2006-01-02 15:04"), startTime.Format("2006-01-02 15:04"))
				}
			case endDateStr != "":
				return errInvalidInputf("--end-date requires --end-time for timed events")
			default:
				dur := time.Hour
				if durationStr != "" {
					dur, err = parseCLIDuration("duration", durationStr)
					if err != nil {
						return err
					}
				}
				endTime = startTime.Add(dur)
			}

			// For timed events, pass startTime so date-only EXDATE/RDATE
			// values inherit the event's time (RFC 5545 Section 3.8.5.1).
			// For all-day events, pass zero time to keep date-only semantics.
			var exrdateRef time.Time
			if !allDay {
				exrdateRef = startTime
			}
			parsedExDates, err := parseDateFlags(exdates, timezone, exrdateRef)
			if err != nil {
				return errInvalidInputf("--exception-date-times: %v", err)
			}
			parsedRDates, err := parseDateFlags(rdates, timezone, exrdateRef)
			if err != nil {
				return errInvalidInputf("--recurrence-date-times: %v", err)
			}

			// Validate all parseable flags before creating the event so a
			// validation failure cannot leave an orphaned row in the database.
			var attachments []model.Attachment
			if len(attachFlags) > 0 {
				attachments, err = parseAttachFlags(attachFlags)
				if err != nil {
					return err
				}
			}
			var alarms []model.Alarm
			if len(alarmFlags) > 0 {
				alarms, err = parseAlarmFlags(alarmFlags)
				if err != nil {
					return err
				}
			}
			var relations []model.Relation
			if len(relationFlags) > 0 {
				relations, err = parseRelationFlags(relationFlags)
				if err != nil {
					return err
				}
			}

			e, err := a.Events.Create(ctx, event.CreateParams{
				CalendarID:     calID,
				Title:          args[0],
				Description:    description,
				Location:       location,
				StartTime:      startTime,
				EndTime:        endTime,
				AllDay:         allDay,
				Status:         status,
				URL:            url,
				Categories:     categories,
				Class:          class,
				Transp:         transp,
				Priority:       priority,
				RecurrenceRule: rrule,
				Timezone:       timezone,
				Geo:            geo,
				ExDates:        parsedExDates,
				RDates:         parsedRDates,
			})
			if err != nil {
				return fmt.Errorf("create event: %w", err)
			}

			if len(attachments) > 0 {
				if err := a.Events.ReplaceAttachments(ctx, e.ID, attachments); err != nil {
					return fmt.Errorf("add attachments: %w", err)
				}
			}

			if len(alarms) > 0 {
				if err := a.Events.ReplaceAlarms(ctx, e.ID, alarms); err != nil {
					return fmt.Errorf("add alarms: %w", err)
				}
			}

			if len(attendeeFlags) > 0 || organizer != "" {
				attendees := parseAttendeeFlags(attendeeFlags)
				if organizer != "" {
					attendees = append(attendees, parseOrganizerFlag(organizer))
				}
				if err := a.Events.ReplaceAttendees(ctx, e.ID, attendees); err != nil {
					return fmt.Errorf("add attendees: %w", err)
				}
			}

			if len(commentFlags) > 0 {
				if err := a.Events.ReplaceComments(ctx, e.ID, commentFlags); err != nil {
					return fmt.Errorf("add comments: %w", err)
				}
			}

			if len(contactFlags) > 0 {
				if err := a.Events.ReplaceContacts(ctx, e.ID, contactFlags); err != nil {
					return fmt.Errorf("add contacts: %w", err)
				}
			}

			if len(resourceFlags) > 0 {
				if err := a.Events.ReplaceResources(ctx, e.ID, resourceFlags); err != nil {
					return fmt.Errorf("add resources: %w", err)
				}
			}

			if len(relations) > 0 {
				if err := a.Events.ReplaceRelations(ctx, e.ID, relations); err != nil {
					return fmt.Errorf("add relations: %w", err)
				}
			}

			// Re-read event with related data so JSON output is complete.
			populateEventFields(ctx, a.Events, &e)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, toJSONEvent(e)); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, e.CalendarID, io.Discard)
				return nil
			}
			fmt.Fprintf(w, "Created: %s %s\n", safeText(e.Title), formatEventWhen(e.StartTime, endTime, allDay))
			printDetailInt(w, 10, "id", e.ID)
			printDetailField(w, 10, "uid", e.UID)
			pushCalendarAfterWrite(a, e.CalendarID, w)
			return nil
		},
	}
	cmd.Flags().StringVar(&dateStr, "date", "", "event date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&timeStr, "time", "", "start time (HH:MM); omit for an all-day event")
	cmd.Flags().StringVar(&endDateStr, "end-date", "", "end date (YYYY-MM-DD); for all-day it is the last day inclusive, for timed events must be paired with --end-time")
	cmd.Flags().StringVar(&endTimeStr, "end-time", "", "end time (HH:MM, alternative to --duration; ignored for all-day)")
	cmd.Flags().StringVar(&durationStr, "duration", "1h", "event duration (e.g. 30m, 1h30m; ignored for all-day)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "calendar name (default: first available)")
	cmd.Flags().StringVar(&location, "location", "", "event location")
	cmd.Flags().StringVar(&description, "description", "", "event description")
	cmd.Flags().StringVar(&status, "status", "", "event status (TENTATIVE, CONFIRMED, CANCELLED; default: CONFIRMED)")
	cmd.Flags().StringVar(&url, "url", "", "associated URL")
	cmd.Flags().StringVar(&categories, "categories", "", "comma-separated categories (e.g. work,meeting)")
	cmd.Flags().StringVar(&class, "class", "", "classification (PUBLIC, PRIVATE, CONFIDENTIAL; default: PUBLIC)")
	cmd.Flags().StringVar(&transp, "transparency", "", "free/busy visibility (OPAQUE=busy, TRANSPARENT=free; default: OPAQUE)")
	cmd.Flags().Int64Var(&priority, "priority", 0, "priority 0-9 (0=undefined, 1=highest, 9=lowest)")
	cmd.Flags().StringVar(&rrule, "recurrence-rule", "", "RFC 5545 recurrence rule (e.g. FREQ=DAILY, FREQ=WEEKLY;BYDAY=MO,WE,FR, FREQ=MONTHLY;COUNT=12; alias: --rrule)")
	cmd.Flags().StringVar(&rrule, "rrule", "", "alias for --recurrence-rule")
	cmd.Flags().MarkHidden("rrule")
	cmd.Flags().StringVar(&timezone, "timezone", "", "IANA timezone (e.g. America/New_York, Europe/London, Asia/Tokyo)")
	cmd.Flags().StringVar(&geo, "geo", "", "geographic position (lat;lon, e.g. 37.386;-122.083)")
	cmd.Flags().StringArrayVar(&exdates, "exception-date-times", nil, "exclude date from recurrence (YYYY-MM-DD or YYYY-MM-DDTHH:MM, repeatable; alias: --exdate)")
	cmd.Flags().StringArrayVar(&rdates, "recurrence-date-times", nil, "add extra occurrence date (YYYY-MM-DD or YYYY-MM-DDTHH:MM, repeatable; alias: --rdate)")
	cmd.Flags().StringArrayVar(&exdates, "exdate", nil, "alias for --exception-date-times")
	cmd.Flags().StringArrayVar(&rdates, "rdate", nil, "alias for --recurrence-date-times")
	cmd.Flags().MarkHidden("exdate")
	cmd.Flags().MarkHidden("rdate")
	cmd.Flags().StringArrayVar(&attachFlags, "attach", nil, "attachment (file path or URL; prefix mime/type: for explicit MIME, e.g. application/pdf:/path/to/file; repeatable)")
	cmd.Flags().StringArrayVar(&alarmFlags, "alarm", nil, `alarm in format [ACTION:]TRIGGER[:DESC:REPEAT:DURATION:RELATED:ATTENDEES]; ACTION is DISPLAY (default), EMAIL, or AUDIO; extended fields are optional, e.g. "DISPLAY:-PT30M::3:PT5M:END" or "EMAIL:-PT1H:::::user@example.com"; repeatable`)
	cmd.Flags().StringArrayVar(&attendeeFlags, "attendee", nil, "attendee as email or \"Name <email>\" (defaults: RSVP=NEEDS-ACTION, ROLE=REQ-PARTICIPANT; repeatable)")
	cmd.Flags().StringVar(&organizer, "organizer", "", "event organizer as email or \"Name <email>\" (RFC 5545 ORGANIZER; exported as ROLE=CHAIR)")
	cmd.Flags().StringArrayVar(&commentFlags, "comment", nil, "comment annotation (free-form text, repeatable)")
	cmd.Flags().StringArrayVar(&contactFlags, "contact", nil, "contact info (free-form text, e.g. \"Alice, 555-1234\"; RFC 5545 CONTACT; repeatable)")
	cmd.Flags().StringArrayVar(&resourceFlags, "resource", nil, "resource needed (e.g. PROJECTOR, WHITEBOARD; RFC 5545 RESOURCES; repeatable)")
	cmd.Flags().StringArrayVar(&relationFlags, "related-to", nil, "related event UID, optionally prefixed with PARENT:, CHILD:, or SIBLING: (default: PARENT; RFC 5545 RELATED-TO; repeatable)")
	return cmd
}

func eventUpdateCmd() *cobra.Command {
	var (
		title         string
		dateStr       string
		timeStr       string
		endDateStr    string
		endTimeStr    string
		durationStr   string
		calendarName  string
		location      string
		description   string
		status        string
		url           string
		categories    string
		class         string
		transp        string
		priority      int64
		rrule         string
		timezone      string
		geo           string
		exdates       []string
		rdates        []string
		attachFlags   []string
		alarmFlags    []string
		attendeeFlags []string
		commentFlags  []string
		contactFlags  []string
		resourceFlags []string
		relationFlags []string
		recurrenceID  string
		organizer     string
	)
	cmd := &cobra.Command{
		Use:   "update <id|uid>",
		Short: "Update an existing event",
		Long: `Update an existing event by numeric ID or UID.

Only the flags you pass are changed; other fields keep their current
values. Repeatable flags such as --alarm, --attendee, --resource, and
--related-to replace the full existing set when specified.`,
		Example: `  chroncal event update 42 --title "Demo with customer"
  chroncal event update release-meeting --date 2026-04-11 --time 15:00
  chroncal event update standup-uid --recurrence-id 2026-04-07T12:00:00Z --location "Room 4B"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			existing, err := resolveEvent(ctx, a, args[0], recurrenceID)
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
				ConferenceURI:  existing.ConferenceURI,
				Categories:     existing.Categories,
				ExDates:        existing.ExDates,
				RDates:         existing.RDates,
				Geo:            existing.Geo,
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
			if cmd.Flags().Changed("transparency") {
				p.Transp = transp
			}
			if cmd.Flags().Changed("priority") {
				p.Priority = priority
			}
			if cmd.Flags().Changed("recurrence-rule") || cmd.Flags().Changed("rrule") {
				p.RecurrenceRule = rrule
			}
			if cmd.Flags().Changed("timezone") {
				p.Timezone = timezone
			}
			if cmd.Flags().Changed("geo") {
				p.Geo = geo
			}
			// Validate changed enum fields.
			valStatus, valClass, valTransp, valPriority := "", "", "", int64(0)
			if cmd.Flags().Changed("status") {
				valStatus = status
			}
			if cmd.Flags().Changed("class") {
				valClass = class
			}
			if cmd.Flags().Changed("transparency") {
				valTransp = transp
			}
			if cmd.Flags().Changed("priority") {
				valPriority = priority
			}
			if err := validateEventEnums(valStatus, valClass, valTransp, valPriority); err != nil {
				return err
			}
			if cmd.Flags().Changed("recurrence-rule") || cmd.Flags().Changed("rrule") {
				if err := validateRRule(rrule); err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("geo") {
				if err := validateGeo(geo); err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("url") {
				if err := validateURL(url); err != nil {
					return err
				}
			}

			// Resolve timezone for date/time parsing.
			loc := time.Local
			tz := timezone
			if !cmd.Flags().Changed("timezone") {
				tz = existing.Timezone
			}
			if tz != "" {
				loc, err = time.LoadLocation(tz)
				if err != nil {
					return fmt.Errorf("load timezone: %w", err)
				}
			}

			if cmd.Flags().Changed("date") || cmd.Flags().Changed("time") {
				date := p.StartTime.In(loc)
				if cmd.Flags().Changed("date") {
					d, err := parseCLIDate("date", dateStr, loc)
					if err != nil {
						return err
					}
					date = time.Date(d.Year(), d.Month(), d.Day(), date.Hour(), date.Minute(), 0, 0, loc)
				}
				if cmd.Flags().Changed("time") {
					t, err := parseCLITime("time", timeStr)
					if err != nil {
						return err
					}
					date = time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, loc)
					p.AllDay = false
				}
				p.StartTime = date
			}

			if cmd.Flags().Changed("end-time") && cmd.Flags().Changed("duration") {
				return errInvalidInputf("--end-time and --duration are mutually exclusive")
			}
			if cmd.Flags().Changed("end-date") && cmd.Flags().Changed("duration") {
				return errInvalidInputf("--end-date and --duration are mutually exclusive")
			}

			var endDate time.Time
			if cmd.Flags().Changed("end-date") {
				endDate, err = parseCLIDate("end-date", endDateStr, loc)
				if err != nil {
					return err
				}
			}

			switch {
			case p.AllDay:
				if cmd.Flags().Changed("end-date") {
					if endDate.Before(p.StartTime) {
						return errInvalidInputf("--end-date %s is before start date %s",
							endDateStr, p.StartTime.Format("2006-01-02"))
					}
					p.EndTime = time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
				} else if cmd.Flags().Changed("date") {
					span := max(int(existing.EndTime.Sub(existing.StartTime)/(24*time.Hour)), 1)
					p.EndTime = p.StartTime.AddDate(0, 0, span)
				}
			case cmd.Flags().Changed("end-time"):
				t, err := parseCLITime("end-time", endTimeStr)
				if err != nil {
					return err
				}
				endRef := p.StartTime
				if cmd.Flags().Changed("end-date") {
					endRef = endDate
				}
				p.EndTime = time.Date(endRef.Year(), endRef.Month(), endRef.Day(), t.Hour(), t.Minute(), 0, 0, loc)
				if !p.EndTime.After(p.StartTime) {
					return errInvalidInputf("end %s is not after start %s (use --end-date to cross midnight, or --duration)",
						p.EndTime.Format("2006-01-02 15:04"), p.StartTime.Format("2006-01-02 15:04"))
				}
			case cmd.Flags().Changed("end-date"):
				return errInvalidInputf("--end-date requires --end-time for timed events")
			case cmd.Flags().Changed("duration"):
				dur, err := parseCLIDuration("duration", durationStr)
				if err != nil {
					return err
				}
				p.EndTime = p.StartTime.Add(dur)
			case cmd.Flags().Changed("date") || cmd.Flags().Changed("time"):
				p.EndTime = p.StartTime.Add(existing.EndTime.Sub(existing.StartTime))
			}

			// Parse EXDATE/RDATE after date/time resolution so date-only
			// values inherit the NEW start time, not the old one.
			if cmd.Flags().Changed("exception-date-times") || cmd.Flags().Changed("exdate") {
				var exrdateRef time.Time
				if !p.AllDay {
					exrdateRef = p.StartTime
				}
				parsed, err := parseDateFlags(exdates, tz, exrdateRef)
				if err != nil {
					return errInvalidInputf("--exception-date-times: %v", err)
				}
				p.ExDates = parsed
			}
			if cmd.Flags().Changed("recurrence-date-times") || cmd.Flags().Changed("rdate") {
				var exrdateRef time.Time
				if !p.AllDay {
					exrdateRef = p.StartTime
				}
				parsed, err := parseDateFlags(rdates, tz, exrdateRef)
				if err != nil {
					return errInvalidInputf("--recurrence-date-times: %v", err)
				}
				p.RDates = parsed
			}

			// Validate parseable flags before updating so a validation
			// failure cannot leave the event in a partially-updated state.
			var attachments []model.Attachment
			if cmd.Flags().Changed("attach") {
				attachments, err = parseAttachFlags(attachFlags)
				if err != nil {
					return err
				}
			}
			var alarms []model.Alarm
			if cmd.Flags().Changed("alarm") {
				alarms, err = parseAlarmFlags(alarmFlags)
				if err != nil {
					return err
				}
			}
			var relations []model.Relation
			if cmd.Flags().Changed("related-to") {
				relations, err = parseRelationFlags(relationFlags)
				if err != nil {
					return err
				}
			}

			e, err := a.Events.Update(ctx, existing.ID, p)
			if err != nil {
				return fmt.Errorf("update event: %w", err)
			}

			if cmd.Flags().Changed("attach") {
				if err := a.Events.ReplaceAttachments(ctx, e.ID, attachments); err != nil {
					return fmt.Errorf("update attachments: %w", err)
				}
			}

			if cmd.Flags().Changed("alarm") {
				if err := a.Events.ReplaceAlarms(ctx, e.ID, alarms); err != nil {
					return fmt.Errorf("update alarms: %w", err)
				}
			}

			if cmd.Flags().Changed("attendee") || cmd.Flags().Changed("organizer") {
				attendees := parseAttendeeFlags(attendeeFlags)
				if cmd.Flags().Changed("organizer") && organizer != "" {
					attendees = append(attendees, parseOrganizerFlag(organizer))
				}
				if err := a.Events.ReplaceAttendees(ctx, e.ID, attendees); err != nil {
					return fmt.Errorf("update attendees: %w", err)
				}
			}

			if cmd.Flags().Changed("comment") {
				if err := a.Events.ReplaceComments(ctx, e.ID, commentFlags); err != nil {
					return fmt.Errorf("update comments: %w", err)
				}
			}

			if cmd.Flags().Changed("contact") {
				if err := a.Events.ReplaceContacts(ctx, e.ID, contactFlags); err != nil {
					return fmt.Errorf("update contacts: %w", err)
				}
			}

			if cmd.Flags().Changed("resource") {
				if err := a.Events.ReplaceResources(ctx, e.ID, resourceFlags); err != nil {
					return fmt.Errorf("update resources: %w", err)
				}
			}

			if cmd.Flags().Changed("related-to") {
				if err := a.Events.ReplaceRelations(ctx, e.ID, relations); err != nil {
					return fmt.Errorf("update relations: %w", err)
				}
			}

			// Re-read event with related data so output is complete.
			populateEventFields(ctx, a.Events, &e)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, toJSONEvent(e)); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, e.CalendarID, io.Discard)
				return nil
			}
			printEvent(w, e)
			pushCalendarAfterWrite(a, e.CalendarID, w)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&dateStr, "date", "", "new date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&timeStr, "time", "", "new start time (HH:MM)")
	cmd.Flags().StringVar(&endDateStr, "end-date", "", "new end date (YYYY-MM-DD); all-day: last day inclusive, timed: pair with --end-time")
	cmd.Flags().StringVar(&endTimeStr, "end-time", "", "new end time (HH:MM)")
	cmd.Flags().StringVar(&durationStr, "duration", "", "new duration (e.g. 30m, 1h30m)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "move to calendar (by name)")
	cmd.Flags().StringVar(&location, "location", "", "new location")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&status, "status", "", "new status (TENTATIVE, CONFIRMED, CANCELLED)")
	cmd.Flags().StringVar(&url, "url", "", "new URL")
	cmd.Flags().StringVar(&categories, "categories", "", "new categories (comma-separated)")
	cmd.Flags().StringVar(&class, "class", "", "new classification (PUBLIC, PRIVATE, CONFIDENTIAL)")
	cmd.Flags().StringVar(&transp, "transparency", "", "new free/busy visibility (OPAQUE=busy, TRANSPARENT=free)")
	cmd.Flags().Int64Var(&priority, "priority", 0, "new priority (0-9)")
	cmd.Flags().StringVar(&rrule, "recurrence-rule", "", "new recurrence rule (alias: --rrule)")
	cmd.Flags().StringVar(&rrule, "rrule", "", "alias for --recurrence-rule")
	cmd.Flags().MarkHidden("rrule")
	cmd.Flags().StringVar(&timezone, "timezone", "", "new IANA timezone (e.g. America/New_York)")
	cmd.Flags().StringVar(&geo, "geo", "", "new geographic position (lat;lon)")
	cmd.Flags().StringArrayVar(&exdates, "exception-date-times", nil, "exclude date/time from recurrence (YYYY-MM-DD or YYYY-MM-DDTHH:MM, repeatable, replaces all; alias: --exdate)")
	cmd.Flags().StringArrayVar(&rdates, "recurrence-date-times", nil, "add extra occurrence date/time (YYYY-MM-DD or YYYY-MM-DDTHH:MM, repeatable, replaces all; alias: --rdate)")
	cmd.Flags().StringArrayVar(&exdates, "exdate", nil, "alias for --exception-date-times")
	cmd.Flags().StringArrayVar(&rdates, "rdate", nil, "alias for --recurrence-date-times")
	cmd.Flags().MarkHidden("exdate")
	cmd.Flags().MarkHidden("rdate")
	cmd.Flags().StringArrayVar(&attachFlags, "attach", nil, "attachment (file path or URL, repeatable)")
	cmd.Flags().StringArrayVar(&alarmFlags, "alarm", nil, `alarm in format [ACTION:]TRIGGER[:DESC:REPEAT:DURATION:RELATED:ATTENDEES]; repeatable`)
	cmd.Flags().StringArrayVar(&attendeeFlags, "attendee", nil, "attendee (email or \"Name <email>\", repeatable, replaces all)")
	cmd.Flags().StringVar(&organizer, "organizer", "", "event organizer (email or \"Name <email>\", replaces existing)")
	cmd.Flags().StringArrayVar(&commentFlags, "comment", nil, "comment annotation (repeatable, replaces all)")
	cmd.Flags().StringArrayVar(&contactFlags, "contact", nil, "contact info (free-form text, repeatable, replaces all)")
	cmd.Flags().StringArrayVar(&resourceFlags, "resource", nil, "resource needed (e.g. PROJECTOR, repeatable, replaces all)")
	cmd.Flags().StringArrayVar(&relationFlags, "related-to", nil, "related event UID with optional PARENT:/CHILD:/SIBLING: prefix (repeatable, replaces all)")
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}

func eventDeleteCmd() *cobra.Command {
	var (
		recurrenceID string
		series       bool
	)
	cmd := &cobra.Command{
		Use:   "delete <id|uid>",
		Short: "Delete an event",
		Long: `Delete a single event, a specific recurring override, or an entire
recurring series.`,
		Example: `  chroncal event delete 42
  chroncal event delete standup-uid --recurrence-id 2026-04-07T12:00:00Z
  chroncal event delete standup-uid --series`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			e, err := resolveEvent(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}

			if series && recurrenceID != "" {
				return errInvalidInputf("--series and --recurrence-id are mutually exclusive")
			}

			question := fmt.Sprintf("Delete event %q?", safeText(e.Title))
			if series {
				question = fmt.Sprintf("Delete the entire recurring series %q (master + all overrides)?", safeText(e.Title))
			} else if recurrenceID != "" {
				question = fmt.Sprintf("Delete override instance of %q at %s?", safeText(e.Title), recurrenceID)
			}
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if series {
				if err := a.Events.DeleteSeries(ctx, e.UID); err != nil {
					return fmt.Errorf("delete series: %w", err)
				}
				w := cmd.OutOrStdout()
				if outputFmt != "text" {
					if err := printOutput(w, map[string]any{"deleted": true, "uid": e.UID, "series": true}); err != nil {
						return err
					}
					pushCalendarAfterWrite(a, e.CalendarID, io.Discard)
					return nil
				}
				fmt.Fprintf(w, "Deleted event series %q.\n", safeText(e.UID))
				pushCalendarAfterWrite(a, e.CalendarID, w)
				return nil
			}

			if err := a.Events.Delete(ctx, e.ID); err != nil {
				return fmt.Errorf("delete event: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, map[string]any{"deleted": true, "id": e.ID}); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, e.CalendarID, io.Discard)
				return nil
			}
			fmt.Fprintf(w, "Deleted event %d.\n", e.ID)
			pushCalendarAfterWrite(a, e.CalendarID, w)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	cmd.Flags().BoolVar(&series, "series", false, "delete the entire recurring series (master + all overrides)")
	addConfirmFlag(cmd)
	return cmd
}

// parseDateFlags normalizes date/datetime flag values to RFC 3339 for storage.
// Accepted formats: YYYY-MM-DD, YYYY-MM-DDTHH:MM, RFC 3339.
// When tz is non-empty, it is used as the IANA timezone for interpreting values
// that lack an explicit offset (i.e. not RFC 3339). Falls back to local time.
//
// formatEventWhen renders a human-readable "when" clause for an event,
// handling single-day, multi-day all-day, and cross-midnight timed events.
// The end time is exclusive, matching how we store it internally.
func formatEventWhen(start, end time.Time, allDay bool) string {
	s := start.Local()
	e := end.Local()
	if allDay {
		last := e.AddDate(0, 0, -1)
		if s.Year() == last.Year() && s.YearDay() == last.YearDay() {
			return fmt.Sprintf("on %s (all day)", s.Format("Mon, Jan 2 2006"))
		}
		return fmt.Sprintf("from %s to %s (all day)",
			s.Format("Mon, Jan 2 2006"), last.Format("Mon, Jan 2 2006"))
	}
	if s.Year() == e.Year() && s.YearDay() == e.YearDay() {
		return fmt.Sprintf("on %s at %s - %s",
			s.Format("Mon, Jan 2 2006"), s.Format("15:04"), e.Format("15:04"))
	}
	return fmt.Sprintf("from %s %s to %s %s",
		s.Format("Mon, Jan 2 2006"), s.Format("15:04"),
		e.Format("Mon, Jan 2 2006"), e.Format("15:04"))
}

// startTime is the event's start time. When a date-only value (YYYY-MM-DD) is
// provided for a timed event, the start time's hour and minute are overlaid onto
// the parsed date so that EXDATE/RDATE values match the recurrence instance time
// per RFC 5545 Section 3.8.5.1.
func parseDateFlags(flags []string, tz string, startTime time.Time) (string, error) {
	loc := time.Local
	if tz != "" {
		var err error
		loc, err = time.LoadLocation(tz)
		if err != nil {
			return "", fmt.Errorf("load timezone %q: %w", tz, err)
		}
	}
	var out []string
	for _, val := range flags {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		var t time.Time
		var err error
		dateOnly := false
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02T15:04",
			"2006-01-02",
		} {
			if layout == time.RFC3339 {
				t, err = time.Parse(layout, val)
			} else {
				t, err = time.ParseInLocation(layout, val, loc)
			}
			if err == nil {
				if layout == "2006-01-02" {
					dateOnly = true
				}
				break
			}
		}
		if err != nil {
			return "", errInvalidInputf("parse date %q: expected YYYY-MM-DD or YYYY-MM-DDTHH:MM", val)
		}
		// For date-only values on timed events, overlay the event's start
		// time so that the EXDATE/RDATE matches the recurrence instance.
		if dateOnly && !startTime.IsZero() {
			stIn := startTime.In(loc)
			t = time.Date(t.Year(), t.Month(), t.Day(), stIn.Hour(), stIn.Minute(), stIn.Second(), 0, loc)
		}
		// For date-only values with no start time (todos), preserve
		// the date-only format so export emits VALUE=DATE correctly.
		if dateOnly && startTime.IsZero() {
			out = append(out, t.Format("2006-01-02"))
		} else {
			out = append(out, t.UTC().Format(time.RFC3339))
		}
	}
	return strings.Join(out, ","), nil
}

// parseOrganizerFlag parses the --organizer flag into an Attendee with Organizer=true.
// Accepts email or "Name <email>".
func parseOrganizerFlag(val string) model.Attendee {
	var name, email string
	if idx := strings.Index(val, "<"); idx >= 0 {
		name = strings.TrimSpace(val[:idx])
		email = strings.TrimRight(val[idx+1:], ">")
	} else {
		email = val
	}
	return model.Attendee{
		Email:      email,
		Name:       name,
		Role:       "CHAIR",
		RSVPStatus: "ACCEPTED",
		Organizer:  true,
	}
}

// parseRelationFlags parses --related-to flag values into Relation models.
// Each value can be:
//   - A UID: "some-event-uid" (defaults to RELTYPE=PARENT)
//   - "RELTYPE:uid": "PARENT:uid", "CHILD:uid", "SIBLING:uid"
func parseRelationFlags(flags []string) ([]model.Relation, error) {
	validTypes := map[string]bool{"PARENT": true, "CHILD": true, "SIBLING": true}
	var out []model.Relation
	for _, val := range flags {
		relType := "PARENT"
		uid := val
		if idx := strings.Index(val, ":"); idx > 0 {
			prefix := strings.ToUpper(val[:idx])
			if validTypes[prefix] {
				relType = prefix
				uid = val[idx+1:]
			}
		}
		if uid == "" {
			return nil, errInvalidInputf("--related-to %q: UID must not be empty", val)
		}
		out = append(out, model.Relation{RelType: relType, RelUID: uid})
	}
	return out, nil
}

// parseAttendeeFlags parses --attendee flag values into Attendee models.
// Each value can be:
//   - An email address: "user@example.com"
//   - "Name <email>": "Alice <alice@example.com>"
func parseAttendeeFlags(flags []string) []model.Attendee {
	out := make([]model.Attendee, 0, len(flags))
	for _, val := range flags {
		var name, email string
		if idx := strings.Index(val, "<"); idx >= 0 {
			name = strings.TrimSpace(val[:idx])
			email = strings.TrimRight(val[idx+1:], ">")
		} else {
			email = val
		}
		out = append(out, model.Attendee{
			Email:      email,
			Name:       name,
			Role:       "REQ-PARTICIPANT",
			RSVPStatus: "NEEDS-ACTION",
		})
	}
	return out
}

// parseAlarmFlags parses --alarm flag values into Alarm models.
//
// Simple format (backward compatible):
//
//	"-PT15M"              → DISPLAY, 15min before start
//	"EMAIL:-PT1H"         → EMAIL, 1h before start
//
// Extended format (duration triggers only):
//
//	"ACTION:TRIGGER:DESC:REPEAT:DURATION:RELATED:ATTENDEES"
//	"DISPLAY:-PT30M::3:PT5M:END"                          → repeat 3x every 5min, relative to END
//	"EMAIL:-PT1H:::::alice@example.com,bob@example.com"    → EMAIL with attendees
//
// Extended format is only available for duration triggers (starting with -, +, or P).
// Absolute RFC 3339 triggers do not support additional fields.
func parseAlarmFlags(flags []string) ([]model.Alarm, error) {
	var out []model.Alarm
	warnedMissingSMTP := false
	for _, val := range flags {
		a, err := parseOneAlarm(val)
		if err != nil {
			return nil, err
		}
		if a.Action == "EMAIL" && !warnedMissingSMTP && !smtpConfiguredForEmailAlarms() {
			fmt.Fprintf(os.Stderr, "chroncal: warning: EMAIL alarm added without SMTP configuration (set CHRONCAL_SMTP_HOST or [smtp].host); alarm will behave as DISPLAY until SMTP is configured\n")
			warnedMissingSMTP = true
		}
		if a.Action == "EMAIL" && len(a.Attendees) == 0 {
			fmt.Fprintf(os.Stderr, "chroncal: warning: EMAIL alarm has no attendees (RFC 5545 requires at least one; alarm will behave as DISPLAY)\n")
		}
		out = append(out, a)
	}
	return out, nil
}

func smtpConfiguredForEmailAlarms() bool {
	return strings.TrimSpace(cfg.SMTP.Host) != ""
}

func parseOneAlarm(val string) (model.Alarm, error) {
	action := "DISPLAY"
	rest := val

	// Check for ACTION: prefix
	if idx := strings.Index(val, ":"); idx > 0 {
		prefix := strings.ToUpper(val[:idx])
		if prefix == "DISPLAY" || prefix == "EMAIL" || prefix == "AUDIO" {
			action = prefix
			rest = val[idx+1:]
		}
	}

	if rest == "" {
		return model.Alarm{}, fmt.Errorf("alarm %q: missing trigger value", val)
	}

	// Determine if the trigger is a duration (can have extended fields) or
	// an absolute datetime (no extended fields, since RFC 3339 contains colons).
	isDuration := rest[0] == '-' || rest[0] == '+' || rest[0] == 'P'

	a := model.Alarm{
		Action:      action,
		Description: "Reminder",
		Related:     "START",
	}

	if !isDuration {
		// Absolute trigger — no splitting on colons.
		if err := validateAlarmTrigger(rest); err != nil {
			return model.Alarm{}, fmt.Errorf("alarm %q: %w", val, err)
		}
		// Normalize RFC 3339 to iCal UTC format for consistent storage.
		if t, err := time.Parse(time.RFC3339, rest); err == nil {
			a.TriggerValue = t.UTC().Format("20060102T150405Z")
		} else {
			a.TriggerValue = rest // Already iCal format or other valid form.
		}
		return a, nil
	}

	// Duration trigger — split into positional fields.
	// Fields: trigger, description, repeat, duration, related, attendees
	parts := strings.SplitN(rest, ":", 6)
	a.TriggerValue = parts[0]

	if err := validateAlarmTrigger(a.TriggerValue); err != nil {
		return model.Alarm{}, fmt.Errorf("alarm %q: %w", val, err)
	}

	// Parse optional fields from the extended format.
	if len(parts) > 1 && parts[1] != "" {
		a.Description = parts[1]
	}
	if len(parts) > 2 && parts[2] != "" {
		r, err := strconv.Atoi(parts[2])
		if err != nil {
			return model.Alarm{}, fmt.Errorf("alarm %q: invalid repeat count %q", val, parts[2])
		}
		a.Repeat = r
	}
	if len(parts) > 3 && parts[3] != "" {
		if err := duration.Validate(parts[3]); err != nil {
			return model.Alarm{}, fmt.Errorf("alarm %q: invalid repeat duration %q", val, parts[3])
		}
		a.Duration = parts[3]
	}
	if len(parts) > 4 && parts[4] != "" {
		rel := strings.ToUpper(parts[4])
		if rel != "START" && rel != "END" {
			return model.Alarm{}, fmt.Errorf("alarm %q: related must be START or END, got %q", val, parts[4])
		}
		a.Related = rel
	}
	if len(parts) > 5 && parts[5] != "" {
		for _, email := range strings.Split(parts[5], ",") {
			email = strings.TrimSpace(email)
			if email == "" {
				continue
			}
			if !strings.Contains(email, "@") {
				return model.Alarm{}, fmt.Errorf("alarm %q: invalid attendee email %q", val, email)
			}
			a.Attendees = append(a.Attendees, model.AlarmAttendee{Email: email})
		}
	}

	// Cross-field validation per RFC 5545.
	if (a.Repeat > 0) != (a.Duration != "") {
		return model.Alarm{}, fmt.Errorf("alarm %q: REPEAT and DURATION must be specified together", val)
	}

	return a, nil
}

// parseAttachFlags parses --attach flag values into Attachment models.
// Each value can be:
//   - A file path (read as blob, MIME inferred from extension)
//   - "mime/type:path" (blob with explicit MIME)
//   - A URL containing "://" (URI attachment)
//   - "mime/type:url" (URI with explicit MIME)
func parseAttachFlags(flags []string) ([]model.Attachment, error) {
	var out []model.Attachment
	for _, val := range flags {
		var fmttype, target string

		// Check for explicit MIME prefix like "application/pdf:/path/to/file"
		if idx := strings.Index(val, ":"); idx > 0 {
			prefix := val[:idx]
			if strings.Contains(prefix, "/") && !strings.Contains(prefix, "://") {
				fmttype = prefix
				target = val[idx+1:]
			} else {
				target = val
			}
		} else {
			target = val
		}

		if strings.Contains(target, "://") {
			// URI attachment
			out = append(out, model.Attachment{URI: target, FmtType: fmttype})
		} else {
			// File path — read as blob
			data, err := os.ReadFile(target)
			if err != nil {
				return nil, fmt.Errorf("read attachment %q: %w", target, err)
			}
			if fmttype == "" {
				fmttype = mime.TypeByExtension(filepath.Ext(target))
			}
			out = append(out, model.Attachment{
				Data:     data,
				Filename: filepath.Base(target),
				FmtType:  fmttype,
			})
		}
	}
	return out, nil
}

// validateEventEnums checks that status, class, transparency, and priority
// values are valid per RFC 5545. Empty strings are allowed (defaults apply).
func validateEventEnums(status, class, transp string, priority int64) error {
	if status != "" {
		switch strings.ToUpper(status) {
		case "TENTATIVE", "CONFIRMED", "CANCELLED":
		default:
			return errInvalidInputf("invalid --status %q: must be TENTATIVE, CONFIRMED, or CANCELLED", status)
		}
	}
	if class != "" {
		switch strings.ToUpper(class) {
		case "PUBLIC", "PRIVATE", "CONFIDENTIAL":
		default:
			return errInvalidInputf("invalid --class %q: must be PUBLIC, PRIVATE, or CONFIDENTIAL", class)
		}
	}
	if transp != "" {
		switch strings.ToUpper(transp) {
		case "OPAQUE", "TRANSPARENT":
		default:
			return errInvalidInputf("invalid --transparency %q: must be OPAQUE or TRANSPARENT", transp)
		}
	}
	if priority < 0 || priority > 9 {
		return errInvalidInputf("invalid --priority %d: must be 0-9", priority)
	}
	return nil
}

// validateRRule checks that an RRULE value contains a valid FREQ per RFC 5545
// Section 3.3.10. Empty string is allowed (optional field).
func validateRRule(rrule string) error {
	if rrule == "" {
		return nil
	}
	validFreqs := map[string]bool{
		"SECONDLY": true, "MINUTELY": true, "HOURLY": true,
		"DAILY": true, "WEEKLY": true, "MONTHLY": true, "YEARLY": true,
	}
	for _, part := range strings.Split(strings.ToUpper(rrule), ";") {
		if strings.HasPrefix(part, "FREQ=") {
			freq := strings.TrimPrefix(part, "FREQ=")
			if !validFreqs[freq] {
				return errInvalidInputf("invalid --rrule FREQ=%s: must be one of SECONDLY, MINUTELY, HOURLY, DAILY, WEEKLY, MONTHLY, YEARLY", freq)
			}
			return nil
		}
	}
	return errInvalidInputf("invalid --rrule %q: must contain FREQ= (e.g. FREQ=WEEKLY;BYDAY=MO)", rrule)
}

// validateGeo checks that a GEO value is "lat;lon" with valid ranges per
// RFC 5545 Section 3.8.1.6. Empty string is allowed (optional field).
func validateGeo(geo string) error {
	if geo == "" {
		return nil
	}
	parts := strings.SplitN(geo, ";", 2)
	if len(parts) != 2 {
		return errInvalidInputf("invalid --geo %q: must be lat;lon (e.g. 37.386;-122.083)", geo)
	}
	lat, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return errInvalidInputf("invalid --geo latitude %q: must be a number", parts[0])
	}
	lon, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return errInvalidInputf("invalid --geo longitude %q: must be a number", parts[1])
	}
	if lat < -90 || lat > 90 {
		return errInvalidInputf("invalid --geo latitude %.6f: must be between -90 and 90", lat)
	}
	if lon < -180 || lon > 180 {
		return errInvalidInputf("invalid --geo longitude %.6f: must be between -180 and 180", lon)
	}
	return nil
}

// validateURL checks that a URL has a scheme per RFC 3986. Empty string is
// allowed (optional field).
func validateURL(u string) error {
	if u == "" {
		return nil
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return errInvalidInputf("invalid --url %q: %v", u, err)
	}
	if parsed.Scheme == "" {
		return errInvalidInputf("invalid --url %q: must include a scheme (e.g. https://example.com)", u)
	}
	return nil
}

// validateAlarmTrigger checks that a trigger value is a valid ISO 8601
// duration (e.g. -PT15M, P1D) or an absolute RFC 3339 datetime per
// RFC 5545 Section 3.8.6.3.
func validateAlarmTrigger(trigger string) error {
	if trigger == "" {
		return errInvalidInputf("alarm trigger must not be empty")
	}
	// Try RFC 3339 absolute datetime first.
	if _, err := time.Parse(time.RFC3339, trigger); err == nil {
		return nil
	}
	// Strict RFC 5545 duration validation.
	if err := duration.Validate(trigger); err != nil {
		return errInvalidInputf("invalid alarm trigger %q: must be an ISO 8601 duration (e.g. -PT15M) or RFC 3339 datetime", trigger)
	}
	return nil
}
