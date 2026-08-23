package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func todoAddCmd() *cobra.Command {
	var (
		dueStr        string
		startStr      string
		durationStr   string
		calendarName  string
		location      string
		description   string
		priority      int64
		status        string
		progress      int64
		class         string
		categories    string
		url           string
		geo           string
		rrule         string
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
		Use:   `add "<summary>"`,
		Short: "Create a new todo",
		Long: `Create a new todo in the calendar.

Due and start dates are date-only (YYYY-MM-DD) and stored without a time
component, so they export correctly as VALUE=DATE in iCal regardless of
your timezone.

Duration accepts Go format (1h30m) or RFC 5545 format (PT1H30M).
Note: per RFC 5545, DUE and DURATION are mutually exclusive in a VTODO.

Defaults: status=NEEDS-ACTION, class=PUBLIC, calendar=Personal.
Attendees default to PARTSTAT=NEEDS-ACTION and ROLE=REQ-PARTICIPANT.
Alarms default to ACTION=DISPLAY unless prefixed (e.g. EMAIL:-PT1H).

Setting --status COMPLETED automatically sets the completion timestamp
and percent-complete to 100.`,
		Example: `  # Simple todo with due date
  chroncal todo add "Write quarterly report" --due 2026-04-15

  # Todo with progress tracking and classification
  chroncal todo add "Review security audit" --due 2026-04-10 \
    --status IN-PROCESS --progress 25 --class CONFIDENTIAL

  # Recurring weekly todo with alarm
  chroncal todo add "Team standup prep" --due 2026-04-01 \
    --rrule "FREQ=WEEKLY;BYDAY=MO" --alarm "-PT30M"

  # Todo with attendee and organizer
  chroncal todo add "Review PR #42" --due 2026-04-05 \
    --attendee "Alice <alice@example.com>" \
    --organizer "Bob <bob@example.com>"

  # Todo with categories, comment, and related task
  chroncal todo add "Deploy v2.0" --due 2026-04-20 \
    --categories "release,ops" --comment "Needs QA sign-off" \
    --related-to "PARENT:sprint-planning-uid"

  # Todo with start date and estimated duration
  chroncal todo add "Database migration" --start 2026-04-10 \
    --duration 4h --priority 1`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			if strings.TrimSpace(args[0]) == "" {
				return errInvalidInputf("todo summary must not be empty")
			}

			calID, err := resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}

			// Validate enums
			if status != "" {
				switch strings.ToUpper(status) {
				case "NEEDS-ACTION", "IN-PROCESS", "COMPLETED", "CANCELLED":
				default:
					return errInvalidInputf("invalid --status %q: must be NEEDS-ACTION, IN-PROCESS, COMPLETED, or CANCELLED", status)
				}
			}
			if class != "" {
				switch strings.ToUpper(class) {
				case "PUBLIC", "PRIVATE", "CONFIDENTIAL":
				default:
					return errInvalidInputf("invalid --class %q: must be PUBLIC, PRIVATE, or CONFIDENTIAL", class)
				}
			}
			if progress < 0 || progress > 100 {
				return errInvalidInputf("invalid --progress %d: must be 0-100", progress)
			}
			if priority < 0 || priority > 9 {
				return errInvalidInputf("invalid --priority %d: must be 0-9", priority)
			}
			if err := validateRRule(rrule); err != nil {
				return err
			}
			if err := validateURL(url); err != nil {
				return err
			}
			if err := validateGeo(geo); err != nil {
				return err
			}

			var dueDate string
			if dueStr != "" {
				if _, err := time.Parse("2006-01-02", dueStr); err != nil {
					return errInvalidInputf("parse due date: expected YYYY-MM-DD, got %q", dueStr)
				}
				dueDate = dueStr
			}

			var startDate string
			if startStr != "" {
				if _, err := time.Parse("2006-01-02", startStr); err != nil {
					return errInvalidInputf("parse start date: expected YYYY-MM-DD, got %q", startStr)
				}
				startDate = startStr
			}

			var durationVal string
			if durationStr != "" {
				if d, err := time.ParseDuration(durationStr); err == nil {
					durationVal = duration.FromGo(d)
				} else if strings.HasPrefix(strings.ToUpper(durationStr), "P") {
					// Store the canonical upper case. The parser is
					// case-sensitive, so a lowercase value would fail
					// the span rule with a message that contradicts
					// what the user typed.
					durationVal = strings.ToUpper(durationStr)
				} else {
					return errInvalidInputf("parse duration: %q (use Go format like 1h30m or RFC 5545 like PT1H30M)", durationStr)
				}
			}

			if dueDate != "" && durationVal != "" {
				return errInvalidInputf("--due and --duration are mutually exclusive (RFC 5545 §3.6.2)")
			}

			if startDate != "" && dueDate != "" && startDate > dueDate {
				return errInvalidInputf("--start %s is after --due %s (RFC 5545 §3.6.2: DTSTART must be before DUE)", startDate, dueDate)
			}

			if strings.EqualFold(status, "COMPLETED") && progress != 0 && progress != 100 {
				return fmt.Errorf("--status COMPLETED requires 100%% progress, got %d (omit --progress or set it to 100)", progress)
			}

			parsedExDates, err := parseExdateRdateFlags("exception-date-times", exdates, "", time.Time{})
			if err != nil {
				return err
			}
			parsedRDates, err := parseExdateRdateFlags("recurrence-date-times", rdates, "", time.Time{})
			if err != nil {
				return err
			}

			// Validate all parseable flags before creating the todo so a
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
				// after it would leave a todo row the next run
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

			t, err := a.Todos.Create(ctx, todo.CreateParams{
				CalendarID:      calID,
				Summary:         args[0],
				Description:     description,
				Location:        location,
				DueDate:         dueDate,
				StartDate:       startDate,
				Duration:        durationVal,
				Priority:        priority,
				Status:          strings.ToUpper(status),
				PercentComplete: progress,
				Class:           strings.ToUpper(class),
				Categories:      categories,
				URL:             url,
				Geo:             geo,
				RecurrenceRule:  rrule,
				ExDates:         parsedExDates,
				RDates:          parsedRDates,
			})
			if err != nil {
				return fmt.Errorf("create todo: %w", err)
			}

			if len(attachments) > 0 {
				if err := a.Todos.ReplaceAttachments(ctx, t.ID, attachments); err != nil {
					return fmt.Errorf("add attachments: %w", err)
				}
			}
			if len(alarms) > 0 {
				if err := a.Todos.ReplaceAlarms(ctx, t.ID, alarms); err != nil {
					return fmt.Errorf("add alarms: %w", err)
				}
			}
			if len(attendeeFlags) > 0 || organizer != "" {
				attendees := parseAttendeeFlags(attendeeFlags)
				if organizer != "" {
					attendees = append(attendees, parseOrganizerFlag(organizer))
				}
				if err := a.Todos.ReplaceAttendees(ctx, t.ID, attendees); err != nil {
					return fmt.Errorf("add attendees: %w", err)
				}
			}
			if len(commentFlags) > 0 {
				if err := a.Todos.ReplaceComments(ctx, t.ID, commentFlags); err != nil {
					return fmt.Errorf("add comments: %w", err)
				}
			}
			if len(relations) > 0 {
				if err := a.Todos.ReplaceRelations(ctx, t.ID, relations); err != nil {
					return fmt.Errorf("add relations: %w", err)
				}
			}
			if len(contactFlags) > 0 {
				if err := a.Todos.ReplaceContacts(ctx, t.ID, contactFlags); err != nil {
					return fmt.Errorf("add contacts: %w", err)
				}
			}
			if len(resourceFlags) > 0 {
				if err := a.Todos.ReplaceResources(ctx, t.ID, resourceFlags); err != nil {
					return fmt.Errorf("add resources: %w", err)
				}
			}

			// Re-read related data for output
			populateTodoFields(ctx, a.Todos, &t)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, toJSONTodo(t)); err != nil {
					return err
				}
				opportunisticPush(a, t.CalendarID, cmd)
				return nil
			}
			printTodo(w, t)
			opportunisticPush(a, t.CalendarID, cmd)
			return nil
		},
	}
	cmd.Flags().StringVar(&dueStr, "due", "", "due date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&startStr, "start", "", "start date (YYYY-MM-DD; when the task becomes relevant)")
	cmd.Flags().StringVar(&durationStr, "duration", "", "estimated duration (e.g. 1h30m or PT1H30M)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "calendar name (default: first available)")
	cmd.Flags().StringVar(&location, "location", "", "location")
	cmd.Flags().StringVar(&description, "description", "", "description")
	cmd.Flags().StringVar(&status, "status", "", "status (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED; default: NEEDS-ACTION)")
	cmd.Flags().Int64Var(&progress, "progress", 0, "percent complete (0-100)")
	cmd.Flags().StringVar(&class, "class", "", "classification (PUBLIC, PRIVATE, CONFIDENTIAL; default: PUBLIC)")
	cmd.Flags().Int64Var(&priority, "priority", 0, "priority 0-9 (0=undefined, 1=highest, 9=lowest)")
	cmd.Flags().StringVar(&categories, "categories", "", "comma-separated categories")
	cmd.Flags().StringVar(&url, "url", "", "associated URL")
	cmd.Flags().StringVar(&geo, "geo", "", "geographic position (lat;lon, e.g. 37.386;-122.083)")
	cmd.Flags().StringVar(&rrule, "recurrence-rule", "", "RFC 5545 recurrence rule (e.g. FREQ=WEEKLY;BYDAY=MO)")
	cmd.Flags().StringArrayVar(&exdates, "exception-date-times", nil, "exclude date from recurrence (YYYY-MM-DD, repeatable)")
	cmd.Flags().StringArrayVar(&rdates, "recurrence-date-times", nil, "add extra recurrence date (YYYY-MM-DD, repeatable)")
	cmd.Flags().StringArrayVar(&attachFlags, "attach", nil, "attachment (file path or URL, repeatable)")
	cmd.Flags().StringArrayVar(&alarmFlags, "alarm", nil, alarmFlagHelp)
	cmd.Flags().StringArrayVar(&attendeeFlags, "attendee", nil, "attendee (email or \"Name <email>\"; repeatable)")
	cmd.Flags().StringVar(&organizer, "organizer", "", "organizer (email or \"Name <email>\")")
	cmd.Flags().StringArrayVar(&commentFlags, "comment", nil, "comment annotation (free-form text, repeatable)")
	cmd.Flags().StringArrayVar(&contactFlags, "contact", nil, "contact info (free-form text, e.g. \"Alice, 555-1234\"; repeatable)")
	cmd.Flags().StringArrayVar(&resourceFlags, "resource", nil, "resource needed (e.g. PROJECTOR, WHITEBOARD; repeatable)")
	cmd.Flags().StringArrayVar(&relationFlags, "related-to", nil, "related UID with optional PARENT:/CHILD:/SIBLING: prefix (repeatable)")
	// Aliases
	cmd.Flags().StringVar(&rrule, "rrule", "", "alias for --recurrence-rule")
	cmd.Flags().StringArrayVar(&exdates, "exdate", nil, "alias for --exception-date-times")
	cmd.Flags().StringArrayVar(&rdates, "rdate", nil, "alias for --recurrence-date-times")
	cmd.Flags().Lookup("rrule").Usage = "alias for --recurrence-rule"
	cmd.Flags().Lookup("exdate").Usage = "alias for --exception-date-times"
	cmd.Flags().Lookup("rdate").Usage = "alias for --recurrence-date-times"
	return cmd
}
