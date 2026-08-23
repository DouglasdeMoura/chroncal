package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

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
		clearForeign  bool
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
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			existing, createOverride, err := resolveEventOccurrence(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}
			var instanceTime time.Time
			if createOverride {
				instanceTime, err = parseRFC3339Flag("recurrence-id", recurrenceID)
				if err != nil {
					return err
				}
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
				if dur <= 0 {
					return errInvalidInputf("--duration must be positive (e.g. 30m, 1h), got %q", durationStr)
				}
				p.EndTime = p.StartTime.Add(dur)
			case cmd.Flags().Changed("date") || cmd.Flags().Changed("time"):
				if existing.AllDay && !p.AllDay {
					// All-day → timed conversion with no explicit span:
					// default to 1h like `event add`, not the 24h (or
					// multi-day) all-day duration.
					p.EndTime = p.StartTime.Add(time.Hour)
				} else {
					p.EndTime = p.StartTime.Add(existing.EndTime.Sub(existing.StartTime))
				}
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

			var e event.Event
			if createOverride {
				e, err = a.Events.UpdateInstance(ctx, existing.UID, instanceTime, p)
			} else {
				e, err = a.Events.Update(ctx, existing.ID, p)
			}
			if err != nil {
				return fmt.Errorf("update event: %w", err)
			}

			if cmd.Flags().Changed("attach") {
				if err := a.Events.ReplaceAttachments(ctx, e.ID, attachments); err != nil {
					return fmt.Errorf("update attachments: %w", err)
				}
			}

			switch {
			case cmd.Flags().Changed("alarm") && clearForeign:
				// The flag makes the replacement set the whole set, so a
				// preserved alarm goes with the rows the flags replace.
				if err := a.Events.ReplaceAlarms(ctx, e.ID, alarms); err != nil {
					return fmt.Errorf("update alarms: %w", err)
				}
			case cmd.Flags().Changed("alarm"):
				if err := a.Events.ReplaceFireableAlarms(ctx, e.ID, alarms); err != nil {
					return fmt.Errorf("update alarms: %w", err)
				}
			case clearForeign:
				// The flag on its own removes the preserved rows and keeps
				// the alarms the user can state.
				if err := a.Events.ClearSyncOnlyAlarms(ctx, e.ID); err != nil {
					return fmt.Errorf("clear foreign alarms: %w", err)
				}
			}

			if cmd.Flags().Changed("attendee") || cmd.Flags().Changed("organizer") {
				existingAtt, err := a.Events.ListAttendees(ctx, e.ID)
				if err != nil {
					return fmt.Errorf("load attendees: %w", err)
				}
				attendees := mergeAttendeeUpdate(existingAtt,
					cmd.Flags().Changed("attendee"), parseAttendeeFlags(attendeeFlags),
					cmd.Flags().Changed("organizer"), organizer)
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
				opportunisticPush(a, e.CalendarID, cmd)
				return nil
			}
			printEvent(w, e)
			opportunisticPush(a, e.CalendarID, cmd)
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
	cmd.Flags().StringArrayVar(&alarmFlags, "alarm", nil, alarmFlagHelp)
	cmd.Flags().BoolVar(&clearForeign, "clear-foreign-alarms", false, clearForeignAlarmsHelp)
	cmd.Flags().StringArrayVar(&attendeeFlags, "attendee", nil, "attendee (email or \"Name <email>\", repeatable, replaces all)")
	cmd.Flags().StringVar(&organizer, "organizer", "", "event organizer (email or \"Name <email>\", replaces existing)")
	cmd.Flags().StringArrayVar(&commentFlags, "comment", nil, "comment annotation (repeatable, replaces all)")
	cmd.Flags().StringArrayVar(&contactFlags, "contact", nil, "contact info (free-form text, repeatable, replaces all)")
	cmd.Flags().StringArrayVar(&resourceFlags, "resource", nil, "resource needed (e.g. PROJECTOR, repeatable, replaces all)")
	cmd.Flags().StringArrayVar(&relationFlags, "related-to", nil, "related event UID with optional PARENT:/CHILD:/SIBLING: prefix (repeatable, replaces all)")
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}
