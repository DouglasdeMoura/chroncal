package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

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
		Args: exactOneArg,
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
					if err := mustPositiveDuration("duration", durationStr, dur); err != nil {
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
				// Apply the service write rule here too. The create
				// commits before the alarms are written, so a rejection
				// after it would leave an event row the next run
				// duplicates (issue #585).
				alarms, err = model.PrepareAlarmsForWrite(alarms)
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
				opportunisticPush(a, e.CalendarID, cmd)
				return nil
			}
			printEvent(w, e)
			opportunisticPush(a, e.CalendarID, cmd)
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
	cmd.Flags().StringArrayVar(&alarmFlags, "alarm", nil, alarmFlagHelp)
	cmd.Flags().StringArrayVar(&attendeeFlags, "attendee", nil, "attendee as email or \"Name <email>\" (defaults: RSVP=NEEDS-ACTION, ROLE=REQ-PARTICIPANT; repeatable)")
	cmd.Flags().StringVar(&organizer, "organizer", "", "event organizer as email or \"Name <email>\" (RFC 5545 ORGANIZER; exported as ROLE=CHAIR)")
	cmd.Flags().StringArrayVar(&commentFlags, "comment", nil, "comment annotation (free-form text, repeatable)")
	cmd.Flags().StringArrayVar(&contactFlags, "contact", nil, "contact info (free-form text, e.g. \"Alice, 555-1234\"; RFC 5545 CONTACT; repeatable)")
	cmd.Flags().StringArrayVar(&resourceFlags, "resource", nil, "resource needed (e.g. PROJECTOR, WHITEBOARD; RFC 5545 RESOURCES; repeatable)")
	cmd.Flags().StringArrayVar(&relationFlags, "related-to", nil, "related event UID, optionally prefixed with PARENT:, CHILD:, or SIBLING: (default: PARENT; RFC 5545 RELATED-TO; repeatable)")
	return cmd
}
