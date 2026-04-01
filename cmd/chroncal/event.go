package main

import (
	"context"
	"fmt"
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
)

func eventCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "event",
		Short: "Manage events",
	}
	cmd.AddCommand(eventListCmd(), eventGetCmd(), eventAddCmd(), eventUpdateCmd(), eventDeleteCmd(), eventSearchCmd())
	return cmd
}

func eventListCmd() *cobra.Command {
	var (
		fromStr      string
		toStr        string
		calendarName string
		status       string
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

			var calID int64
			if calendarName != "" {
				calID, err = resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
			}

			events, err := a.Recurrences.ListFilteredEvents(ctx, recurrence.EventListParams{
				CalendarID: calID,
				Status:     status,
				From:       from,
				To:         to,
			})
			if err != nil {
				return fmt.Errorf("list events: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				items := make([]jsonEvent, len(events))
				for i, e := range events {
					items[i] = toJSONEvent(e)
				}
				return printOutput(w, items)
			}
			printEvents(w, events)
			return nil
		},
	}
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 14 days from now)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (TENTATIVE, CONFIRMED, CANCELLED)")
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
		Args:  cobra.ExactArgs(1),
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
			printEvents(w, events)
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
		Args:  cobra.ExactArgs(1),
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

			e.Alarms, _ = a.Events.ListAlarms(ctx, e.ID)
			e.Attendees, _ = a.Events.ListAttendees(ctx, e.ID)
			e.Attachments, _ = a.Events.ListAttachments(ctx, e.ID)
			e.Comments, _ = a.Events.ListComments(ctx, e.ID)
			e.Contacts, _ = a.Events.ListContacts(ctx, e.ID)
			e.Resources, _ = a.Events.ListResources(ctx, e.ID)
			e.Relations, _ = a.Events.ListRelations(ctx, e.ID)

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

Defaults: status=CONFIRMED, class=PUBLIC, transparency=OPAQUE, calendar=Personal.
Attendees default to RSVP=NEEDS-ACTION and ROLE=REQ-PARTICIPANT.
Alarms default to ACTION=DISPLAY unless prefixed (e.g. EMAIL:-PT1H).`,
		Example: `  # Timed event tomorrow at 2pm for 90 minutes
  chroncal event add "Lunch with Alice" --date 2026-04-01 --time 14:00 --duration 1h30m

  # All-day event (no --time flag)
  chroncal event add "Company Holiday" --date 2026-12-25

  # Event with explicit end time instead of duration
  chroncal event add "Workshop" --date 2026-05-10 --time 09:00 --end-time 12:30

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
    --alarm "-PT1H" --alarm "EMAIL:-PT1D" --alarm "AUDIO:-PT5M"

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
				return fmt.Errorf("event title must not be empty")
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
				date, err = time.ParseInLocation("2006-01-02", dateStr, loc)
				if err != nil {
					return fmt.Errorf("parse date: %w", err)
				}
			}

			allDay := timeStr == ""
			startTime := time.Date(date.Year(), date.Month(), date.Day(), 9, 0, 0, 0, loc)
			if timeStr != "" {
				t, err := time.Parse("15:04", timeStr)
				if err != nil {
					return fmt.Errorf("parse time: %w", err)
				}
				startTime = time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			}

			var endTime time.Time
			if endTimeStr != "" {
				t, err := time.Parse("15:04", endTimeStr)
				if err != nil {
					return fmt.Errorf("parse end-time: %w", err)
				}
				endTime = time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, loc)
				if !endTime.After(startTime) {
					return fmt.Errorf("--end-time %s is not after --time %s (use --duration for cross-midnight events)", endTimeStr, timeStr)
				}
			} else {
				dur := time.Hour
				if durationStr != "" {
					dur, err = time.ParseDuration(durationStr)
					if err != nil {
						return fmt.Errorf("parse duration: %w", err)
					}
				}
				endTime = startTime.Add(dur)
			}

			if allDay {
				startTime = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
				endTime = startTime.AddDate(0, 0, 1)
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
				return fmt.Errorf("--exception-date-times: %w", err)
			}
			parsedRDates, err := parseDateFlags(rdates, timezone, exrdateRef)
			if err != nil {
				return fmt.Errorf("--recurrence-date-times: %w", err)
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
			e.Alarms, _ = a.Events.ListAlarms(ctx, e.ID)
			e.Attendees, _ = a.Events.ListAttendees(ctx, e.ID)
			e.Attachments, _ = a.Events.ListAttachments(ctx, e.ID)
			e.Comments, _ = a.Events.ListComments(ctx, e.ID)
			e.Contacts, _ = a.Events.ListContacts(ctx, e.ID)
			e.Resources, _ = a.Events.ListResources(ctx, e.ID)
			e.Relations, _ = a.Events.ListRelations(ctx, e.ID)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONEvent(e))
			}
			if allDay {
				fmt.Fprintf(w, "Created: %s on %s (all day)\n", e.Title, e.StartTime.Local().Format("Mon, Jan 2 2006"))
			} else {
				fmt.Fprintf(w, "Created: %s on %s at %s – %s\n", e.Title, e.StartTime.Local().Format("Mon, Jan 2 2006"), e.StartTime.Local().Format("15:04"), endTime.Local().Format("15:04"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dateStr, "date", "", "event date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&timeStr, "time", "", "start time (HH:MM); omit for an all-day event")
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
		Args:  cobra.ExactArgs(1),
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
					d, err := time.ParseInLocation("2006-01-02", dateStr, loc)
					if err != nil {
						return fmt.Errorf("parse date: %w", err)
					}
					date = time.Date(d.Year(), d.Month(), d.Day(), date.Hour(), date.Minute(), 0, 0, loc)
				}
				if cmd.Flags().Changed("time") {
					t, err := time.Parse("15:04", timeStr)
					if err != nil {
						return fmt.Errorf("parse time: %w", err)
					}
					date = time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, loc)
					p.AllDay = false
				}
				p.StartTime = date
			}

			if cmd.Flags().Changed("end-time") {
				t, err := time.Parse("15:04", endTimeStr)
				if err != nil {
					return fmt.Errorf("parse end-time: %w", err)
				}
				p.EndTime = time.Date(p.StartTime.Year(), p.StartTime.Month(), p.StartTime.Day(), t.Hour(), t.Minute(), 0, 0, loc)
				if !p.EndTime.After(p.StartTime) {
					return fmt.Errorf("--end-time %s is not after start time %s (use --duration for cross-midnight events)",
						endTimeStr, p.StartTime.Format("15:04"))
				}
			} else if cmd.Flags().Changed("duration") {
				dur, err := time.ParseDuration(durationStr)
				if err != nil {
					return fmt.Errorf("parse duration: %w", err)
				}
				p.EndTime = p.StartTime.Add(dur)
			} else if cmd.Flags().Changed("date") || cmd.Flags().Changed("time") {
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
					return fmt.Errorf("--exception-date-times: %w", err)
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
					return fmt.Errorf("--recurrence-date-times: %w", err)
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
			e.Alarms, _ = a.Events.ListAlarms(ctx, e.ID)
			e.Attendees, _ = a.Events.ListAttendees(ctx, e.ID)
			e.Attachments, _ = a.Events.ListAttachments(ctx, e.ID)
			e.Comments, _ = a.Events.ListComments(ctx, e.ID)
			e.Contacts, _ = a.Events.ListContacts(ctx, e.ID)
			e.Resources, _ = a.Events.ListResources(ctx, e.ID)
			e.Relations, _ = a.Events.ListRelations(ctx, e.ID)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONEvent(e))
			}
			printEvent(w, e)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&dateStr, "date", "", "new date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&timeStr, "time", "", "new start time (HH:MM)")
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
		Args:  cobra.ExactArgs(1),
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
				return fmt.Errorf("--series and --recurrence-id are mutually exclusive")
			}

			if series {
				if err := a.Events.DeleteSeries(ctx, e.UID); err != nil {
					return fmt.Errorf("delete series: %w", err)
				}
				w := cmd.OutOrStdout()
				if outputFmt != "text" {
					return printOutput(w, map[string]any{"deleted": true, "uid": e.UID, "series": true})
				}
				fmt.Fprintf(w, "Deleted event series %q.\n", e.UID)
				return nil
			}

			if err := a.Events.Delete(ctx, e.ID); err != nil {
				return fmt.Errorf("delete event: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"deleted": true, "id": e.ID})
			}
			fmt.Fprintf(w, "Deleted event %d.\n", e.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	cmd.Flags().BoolVar(&series, "series", false, "delete the entire recurring series (master + all overrides)")
	return cmd
}

// parseDateFlags normalizes date/datetime flag values to RFC 3339 for storage.
// Accepted formats: YYYY-MM-DD, YYYY-MM-DDTHH:MM, RFC 3339.
// When tz is non-empty, it is used as the IANA timezone for interpreting values
// that lack an explicit offset (i.e. not RFC 3339). Falls back to local time.
//
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
			return "", fmt.Errorf("parse date %q: expected YYYY-MM-DD or YYYY-MM-DDTHH:MM", val)
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
			return nil, fmt.Errorf("--related-to %q: UID must not be empty", val)
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
	var out []model.Attendee
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
	for _, val := range flags {
		a, err := parseOneAlarm(val)
		if err != nil {
			return nil, err
		}
		if a.Action == "EMAIL" && len(a.Attendees) == 0 {
			fmt.Fprintf(os.Stderr, "chroncal: warning: EMAIL alarm has no attendees (RFC 5545 requires at least one; alarm will behave as DISPLAY)\n")
		}
		out = append(out, a)
	}
	return out, nil
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
			return fmt.Errorf("invalid --status %q: must be TENTATIVE, CONFIRMED, or CANCELLED", status)
		}
	}
	if class != "" {
		switch strings.ToUpper(class) {
		case "PUBLIC", "PRIVATE", "CONFIDENTIAL":
		default:
			return fmt.Errorf("invalid --class %q: must be PUBLIC, PRIVATE, or CONFIDENTIAL", class)
		}
	}
	if transp != "" {
		switch strings.ToUpper(transp) {
		case "OPAQUE", "TRANSPARENT":
		default:
			return fmt.Errorf("invalid --transparency %q: must be OPAQUE or TRANSPARENT", transp)
		}
	}
	if priority < 0 || priority > 9 {
		return fmt.Errorf("invalid --priority %d: must be 0-9", priority)
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
				return fmt.Errorf("invalid --rrule FREQ=%s: must be one of SECONDLY, MINUTELY, HOURLY, DAILY, WEEKLY, MONTHLY, YEARLY", freq)
			}
			return nil
		}
	}
	return fmt.Errorf("invalid --rrule %q: must contain FREQ= (e.g. FREQ=WEEKLY;BYDAY=MO)", rrule)
}

// validateGeo checks that a GEO value is "lat;lon" with valid ranges per
// RFC 5545 Section 3.8.1.6. Empty string is allowed (optional field).
func validateGeo(geo string) error {
	if geo == "" {
		return nil
	}
	parts := strings.SplitN(geo, ";", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid --geo %q: must be lat;lon (e.g. 37.386;-122.083)", geo)
	}
	lat, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return fmt.Errorf("invalid --geo latitude %q: must be a number", parts[0])
	}
	lon, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return fmt.Errorf("invalid --geo longitude %q: must be a number", parts[1])
	}
	if lat < -90 || lat > 90 {
		return fmt.Errorf("invalid --geo latitude %.6f: must be between -90 and 90", lat)
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("invalid --geo longitude %.6f: must be between -180 and 180", lon)
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
		return fmt.Errorf("invalid --url %q: %w", u, err)
	}
	if parsed.Scheme == "" {
		return fmt.Errorf("invalid --url %q: must include a scheme (e.g. https://example.com)", u)
	}
	return nil
}

// validateAlarmTrigger checks that a trigger value is a valid ISO 8601
// duration (e.g. -PT15M, P1D) or an absolute RFC 3339 datetime per
// RFC 5545 Section 3.8.6.3.
func validateAlarmTrigger(trigger string) error {
	if trigger == "" {
		return fmt.Errorf("alarm trigger must not be empty")
	}
	// Try RFC 3339 absolute datetime first.
	if _, err := time.Parse(time.RFC3339, trigger); err == nil {
		return nil
	}
	// Strict RFC 5545 duration validation.
	if err := duration.Validate(trigger); err != nil {
		return fmt.Errorf("invalid alarm trigger %q: must be an ISO 8601 duration (e.g. -PT15M) or RFC 3339 datetime", trigger)
	}
	return nil
}
