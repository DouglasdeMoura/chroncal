package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/tcal/internal/duration"
	"github.com/douglasdemoura/tcal/internal/todo"
)

func todoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "todo",
		Short: "Manage todos",
	}
	cmd.AddCommand(todoListCmd(), todoGetCmd(), todoAddCmd(), todoUpdateCmd(), todoDeleteCmd())
	return cmd
}

func todoListCmd() *cobra.Command {
	var (
		calendarName string
		status       string
		all          bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List todos (incomplete by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			var todos []todo.Todo
			switch {
			case all:
				todos, err = a.Todos.ListAll(ctx)
			case status != "":
				todos, err = a.Todos.ListByStatus(ctx, status)
			case calendarName != "":
				calID, cerr := resolveCalendarID(ctx, a, calendarName)
				if cerr != nil {
					return cerr
				}
				todos, err = a.Todos.ListByCalendar(ctx, calID)
			default:
				todos, err = a.Todos.List(ctx)
			}
			if err != nil {
				return fmt.Errorf("list todos: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodos(todos))
			}
			printTodos(w, todos)
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
	cmd.Flags().BoolVar(&all, "all", false, "include completed and cancelled")
	return cmd
}

func todoGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id|uid>",
		Short: "Get todo details by ID or UID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			t, err := resolveTodo(ctx, a, args[0])
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			t.Alarms, _ = a.Todos.ListAlarms(ctx, t.ID)
			t.Attendees, _ = a.Todos.ListAttendees(ctx, t.ID)
			t.Attachments, _ = a.Todos.ListAttachments(ctx, t.ID)
			t.Comments, _ = a.Todos.ListComments(ctx, t.ID)
			t.Contacts, _ = a.Todos.ListContacts(ctx, t.ID)
			t.Resources, _ = a.Todos.ListResources(ctx, t.ID)
			t.Relations, _ = a.Todos.ListRelations(ctx, t.ID)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodo(t))
			}
			printTodo(w, t)
			return nil
		},
	}
	return cmd
}

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
  tcal todo add "Write quarterly report" --due 2026-04-15

  # Todo with progress tracking and classification
  tcal todo add "Review security audit" --due 2026-04-10 \
    --status IN-PROCESS --progress 25 --class CONFIDENTIAL

  # Recurring weekly todo with alarm
  tcal todo add "Team standup prep" --due 2026-04-01 \
    --rrule "FREQ=WEEKLY;BYDAY=MO" --alarm "-PT30M"

  # Todo with attendee and organizer
  tcal todo add "Review PR #42" --due 2026-04-05 \
    --attendee "Alice <alice@example.com>" \
    --organizer "Bob <bob@example.com>"

  # Todo with categories, comment, and related task
  tcal todo add "Deploy v2.0" --due 2026-04-20 \
    --categories "release,ops" --comment "Needs QA sign-off" \
    --related-to "PARENT:sprint-planning-uid"

  # Todo with start date and estimated duration
  tcal todo add "Database migration" --start 2026-04-10 \
    --duration 4h --priority 1`,
		Args: cobra.ExactArgs(1),
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

			// Validate enums
			if status != "" {
				switch strings.ToUpper(status) {
				case "NEEDS-ACTION", "IN-PROCESS", "COMPLETED", "CANCELLED":
				default:
					return fmt.Errorf("invalid --status %q: must be NEEDS-ACTION, IN-PROCESS, COMPLETED, or CANCELLED", status)
				}
			}
			if class != "" {
				switch strings.ToUpper(class) {
				case "PUBLIC", "PRIVATE", "CONFIDENTIAL":
				default:
					return fmt.Errorf("invalid --class %q: must be PUBLIC, PRIVATE, or CONFIDENTIAL", class)
				}
			}
			if progress < 0 || progress > 100 {
				return fmt.Errorf("invalid --progress %d: must be 0-100", progress)
			}
			if priority < 0 || priority > 9 {
				return fmt.Errorf("invalid --priority %d: must be 0-9", priority)
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
					return fmt.Errorf("parse due date: expected YYYY-MM-DD, got %q", dueStr)
				}
				dueDate = dueStr
			}

			var startDate string
			if startStr != "" {
				if _, err := time.Parse("2006-01-02", startStr); err != nil {
					return fmt.Errorf("parse start date: expected YYYY-MM-DD, got %q", startStr)
				}
				startDate = startStr
			}

			var durationVal string
			if durationStr != "" {
				if d, err := time.ParseDuration(durationStr); err == nil {
					durationVal = duration.FromGo(d)
				} else if strings.HasPrefix(strings.ToUpper(durationStr), "P") {
					durationVal = durationStr
				} else {
					return fmt.Errorf("parse duration: %q (use Go format like 1h30m or RFC 5545 like PT1H30M)", durationStr)
				}
			}

			if dueDate != "" && durationVal != "" {
				return fmt.Errorf("--due and --duration are mutually exclusive (RFC 5545 §3.6.2)")
			}

			if startDate != "" && dueDate != "" && startDate > dueDate {
				return fmt.Errorf("--start %s is after --due %s (RFC 5545 §3.6.2: DTSTART must be before DUE)", startDate, dueDate)
			}

			if strings.EqualFold(status, "COMPLETED") && progress != 0 && progress != 100 {
				return fmt.Errorf("--status COMPLETED requires 100%% progress, got %d (omit --progress or set it to 100)", progress)
			}

			parsedExDates, err := parseDateFlags(exdates, "", time.Time{})
			if err != nil {
				return fmt.Errorf("--exdate: %w", err)
			}
			parsedRDates, err := parseDateFlags(rdates, "", time.Time{})
			if err != nil {
				return fmt.Errorf("--rdate: %w", err)
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

			if len(attachFlags) > 0 {
				attachments, err := parseAttachFlags(attachFlags)
				if err != nil {
					return err
				}
				if err := a.Todos.ReplaceAttachments(ctx, t.ID, attachments); err != nil {
					return fmt.Errorf("add attachments: %w", err)
				}
			}
			if len(alarmFlags) > 0 {
				alarms, err := parseAlarmFlags(alarmFlags)
				if err != nil {
					return err
				}
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
			if len(relationFlags) > 0 {
				relations, err := parseRelationFlags(relationFlags)
				if err != nil {
					return err
				}
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
			t.Alarms, _ = a.Todos.ListAlarms(ctx, t.ID)
			t.Attendees, _ = a.Todos.ListAttendees(ctx, t.ID)
			t.Attachments, _ = a.Todos.ListAttachments(ctx, t.ID)
			t.Comments, _ = a.Todos.ListComments(ctx, t.ID)
			t.Contacts, _ = a.Todos.ListContacts(ctx, t.ID)
			t.Resources, _ = a.Todos.ListResources(ctx, t.ID)
			t.Relations, _ = a.Todos.ListRelations(ctx, t.ID)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodo(t))
			}
			msg := fmt.Sprintf("Created: %s", t.Summary)
			if dueDate != "" {
				msg += fmt.Sprintf(" (due %s)", t.ParseDueDate().Format("Jan 2"))
			}
			fmt.Fprintln(w, msg)
			return nil
		},
	}
	cmd.Flags().StringVar(&dueStr, "due", "", "due date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&startStr, "start", "", "start date (YYYY-MM-DD; when the task becomes relevant)")
	cmd.Flags().StringVar(&durationStr, "duration", "", "estimated duration (e.g. 1h30m or PT1H30M)")
	cmd.Flags().StringVar(&calendarName, "calendar", "Personal", "calendar name")
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
	cmd.Flags().StringArrayVar(&alarmFlags, "alarm", nil, `alarm in format [ACTION:]TRIGGER[:DESC:REPEAT:DURATION:RELATED:ATTENDEES]; repeatable`)
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

func todoUpdateCmd() *cobra.Command {
	var (
		summary       string
		dueStr        string
		startStr      string
		durationStr   string
		status        string
		progress      int64
		calendarName  string
		location      string
		description   string
		priority      int64
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
		Use:   "update <id|uid>",
		Short: "Update an existing todo",
		Long: `Update an existing todo by numeric ID or UID.

Only the flags you pass are changed; all other fields keep their current
values. Use an empty string to clear optional fields like --due, --start,
--duration, --description, --location, --url, --categories, or --rrule.

Repeatable flags (--alarm, --attendee, --comment, --contact, --resource,
--attach, --related-to) replace all existing values when specified.

Per RFC 5545, DUE and DURATION are mutually exclusive. To switch from one
to the other, clear the current one first (e.g. --due "" --duration 2h).

Setting --status COMPLETED automatically sets the completion timestamp
and percent-complete to 100. You cannot combine --status COMPLETED with
a --progress value other than 100.`,
		Example: `  # Change the summary
  tcal todo update 1 --summary "Updated task name"

  # Reschedule a todo
  tcal todo update 1 --due 2026-05-01 --start 2026-04-15

  # Mark as completed
  tcal todo update 1 --status COMPLETED

  # Switch from due date to estimated duration
  tcal todo update 1 --due "" --duration 4h

  # Update attendees and add a comment
  tcal todo update 1 \
    --attendee "Alice <alice@example.com>" \
    --comment "Discussed in standup"

  # Move to a different calendar and change classification
  tcal todo update 1 --calendar Work --class CONFIDENTIAL

  # Clear the location
  tcal todo update 1 --location ""`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			existing, err := resolveTodo(ctx, a, args[0])
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			p := todo.UpdateParams{
				Summary:         existing.Summary,
				Description:     existing.Description,
				Location:        existing.Location,
				DueDate:         existing.DueDate,
				StartDate:       existing.StartDate,
				Duration:        existing.Duration,
				CompletedAt:     existing.CompletedAt,
				PercentComplete: existing.PercentComplete,
				Status:          existing.Status,
				CalendarID:      existing.CalendarID,
				Priority:        existing.Priority,
				Class:           existing.Class,
				URL:             existing.URL,
				Categories:      existing.Categories,
				RecurrenceRule:  existing.RecurrenceRule,
				Timezone:        existing.Timezone,
				ExDates:         existing.ExDates,
				RDates:          existing.RDates,
			}

			if cmd.Flags().Changed("summary") {
				p.Summary = summary
			}
			if cmd.Flags().Changed("description") {
				p.Description = description
			}
			if cmd.Flags().Changed("location") {
				p.Location = location
			}
			if cmd.Flags().Changed("due") {
				if dueStr == "" {
					p.DueDate = ""
				} else if _, err := time.Parse("2006-01-02", dueStr); err != nil {
					return fmt.Errorf("parse due date: expected YYYY-MM-DD or empty to clear, got %q", dueStr)
				} else {
					p.DueDate = dueStr
				}
			}
			if cmd.Flags().Changed("start") {
				if startStr == "" {
					p.StartDate = ""
				} else if _, err := time.Parse("2006-01-02", startStr); err != nil {
					return fmt.Errorf("parse start date: expected YYYY-MM-DD or empty to clear, got %q", startStr)
				} else {
					p.StartDate = startStr
				}
			}
			if cmd.Flags().Changed("duration") {
				if durationStr == "" {
					p.Duration = ""
				} else if d, err := time.ParseDuration(durationStr); err == nil {
					p.Duration = duration.FromGo(d)
				} else if strings.HasPrefix(strings.ToUpper(durationStr), "P") {
					p.Duration = durationStr
				} else {
					return fmt.Errorf("parse duration: %q (use Go format like 1h30m or RFC 5545 like PT1H30M)", durationStr)
				}
			}
			if cmd.Flags().Changed("status") {
				switch strings.ToUpper(status) {
				case "NEEDS-ACTION", "IN-PROCESS", "COMPLETED", "CANCELLED":
				default:
					return fmt.Errorf("invalid --status %q: must be NEEDS-ACTION, IN-PROCESS, COMPLETED, or CANCELLED", status)
				}
				p.Status = strings.ToUpper(status)
				if strings.ToUpper(status) == "COMPLETED" {
					p.CompletedAt = time.Now().UTC().Format(time.RFC3339)
					p.PercentComplete = 100
				}
			}
			if cmd.Flags().Changed("progress") {
				if progress < 0 || progress > 100 {
					return fmt.Errorf("invalid --progress %d: must be 0-100", progress)
				}
				p.PercentComplete = progress
			}
			if cmd.Flags().Changed("class") {
				switch strings.ToUpper(class) {
				case "PUBLIC", "PRIVATE", "CONFIDENTIAL":
				default:
					return fmt.Errorf("invalid --class %q: must be PUBLIC, PRIVATE, or CONFIDENTIAL", class)
				}
				p.Class = strings.ToUpper(class)
			}
			if cmd.Flags().Changed("calendar") {
				calID, err := resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
				p.CalendarID = calID
			}
			if cmd.Flags().Changed("priority") {
				if priority < 0 || priority > 9 {
					return fmt.Errorf("invalid --priority %d: must be 0-9", priority)
				}
				p.Priority = priority
			}
			if cmd.Flags().Changed("categories") {
				p.Categories = categories
			}
			if cmd.Flags().Changed("url") {
				if err := validateURL(url); err != nil {
					return err
				}
				p.URL = url
			}
			if cmd.Flags().Changed("geo") {
				if err := validateGeo(geo); err != nil {
					return err
				}
				p.Geo = geo
			}
			if cmd.Flags().Changed("recurrence-rule") || cmd.Flags().Changed("rrule") {
				if err := validateRRule(rrule); err != nil {
					return err
				}
				p.RecurrenceRule = rrule
			}
			if cmd.Flags().Changed("exception-date-times") || cmd.Flags().Changed("exdate") {
				parsed, err := parseDateFlags(exdates, "", time.Time{})
				if err != nil {
					return fmt.Errorf("--exdate: %w", err)
				}
				p.ExDates = parsed
			}
			if cmd.Flags().Changed("recurrence-date-times") || cmd.Flags().Changed("rdate") {
				parsed, err := parseDateFlags(rdates, "", time.Time{})
				if err != nil {
					return fmt.Errorf("--rdate: %w", err)
				}
				p.RDates = parsed
			}

			if p.DueDate != "" && p.Duration != "" {
				return fmt.Errorf("--due and --duration are mutually exclusive (RFC 5545 §3.6.2)")
			}

			if p.StartDate != "" && p.DueDate != "" && p.StartDate > p.DueDate {
				return fmt.Errorf("--start %s is after --due %s (RFC 5545 §3.6.2: DTSTART must be before DUE)", p.StartDate, p.DueDate)
			}

			if p.Status == "COMPLETED" && cmd.Flags().Changed("progress") && p.PercentComplete != 100 {
				return fmt.Errorf("--status COMPLETED requires 100%% progress, got %d (omit --progress or set it to 100)", p.PercentComplete)
			}

			t, err := a.Todos.Update(ctx, existing.ID, p)
			if err != nil {
				return fmt.Errorf("update todo: %w", err)
			}

			if cmd.Flags().Changed("attach") {
				attachments, err := parseAttachFlags(attachFlags)
				if err != nil {
					return err
				}
				if err := a.Todos.ReplaceAttachments(ctx, t.ID, attachments); err != nil {
					return fmt.Errorf("update attachments: %w", err)
				}
			}
			if cmd.Flags().Changed("alarm") {
				alarms, err := parseAlarmFlags(alarmFlags)
				if err != nil {
					return err
				}
				if err := a.Todos.ReplaceAlarms(ctx, t.ID, alarms); err != nil {
					return fmt.Errorf("update alarms: %w", err)
				}
			}
			if cmd.Flags().Changed("attendee") || cmd.Flags().Changed("organizer") {
				attendees := parseAttendeeFlags(attendeeFlags)
				if organizer != "" {
					attendees = append(attendees, parseOrganizerFlag(organizer))
				}
				if err := a.Todos.ReplaceAttendees(ctx, t.ID, attendees); err != nil {
					return fmt.Errorf("update attendees: %w", err)
				}
			}
			if cmd.Flags().Changed("comment") {
				if err := a.Todos.ReplaceComments(ctx, t.ID, commentFlags); err != nil {
					return fmt.Errorf("update comments: %w", err)
				}
			}
			if cmd.Flags().Changed("related-to") {
				relations, err := parseRelationFlags(relationFlags)
				if err != nil {
					return err
				}
				if err := a.Todos.ReplaceRelations(ctx, t.ID, relations); err != nil {
					return fmt.Errorf("update relations: %w", err)
				}
			}
			if cmd.Flags().Changed("contact") {
				if err := a.Todos.ReplaceContacts(ctx, t.ID, contactFlags); err != nil {
					return fmt.Errorf("update contacts: %w", err)
				}
			}
			if cmd.Flags().Changed("resource") {
				if err := a.Todos.ReplaceResources(ctx, t.ID, resourceFlags); err != nil {
					return fmt.Errorf("update resources: %w", err)
				}
			}

			// Re-read related data for output
			t.Alarms, _ = a.Todos.ListAlarms(ctx, t.ID)
			t.Attendees, _ = a.Todos.ListAttendees(ctx, t.ID)
			t.Attachments, _ = a.Todos.ListAttachments(ctx, t.ID)
			t.Comments, _ = a.Todos.ListComments(ctx, t.ID)
			t.Contacts, _ = a.Todos.ListContacts(ctx, t.ID)
			t.Resources, _ = a.Todos.ListResources(ctx, t.ID)
			t.Relations, _ = a.Todos.ListRelations(ctx, t.ID)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodo(t))
			}
			printTodo(w, t)
			return nil
		},
	}
	cmd.Flags().StringVar(&summary, "summary", "", "new summary")
	cmd.Flags().StringVar(&dueStr, "due", "", "new due date (YYYY-MM-DD; empty to clear)")
	cmd.Flags().StringVar(&startStr, "start", "", "new start date (YYYY-MM-DD; empty to clear)")
	cmd.Flags().StringVar(&durationStr, "duration", "", "new duration (e.g. 1h30m or PT1H30M; empty to clear)")
	cmd.Flags().StringVar(&status, "status", "", "new status (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
	cmd.Flags().Int64Var(&progress, "progress", 0, "percent complete (0-100)")
	cmd.Flags().StringVar(&class, "class", "", "new classification (PUBLIC, PRIVATE, CONFIDENTIAL)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "move to calendar")
	cmd.Flags().StringVar(&location, "location", "", "new location")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().Int64Var(&priority, "priority", 0, "new priority (0-9)")
	cmd.Flags().StringVar(&categories, "categories", "", "new categories")
	cmd.Flags().StringVar(&url, "url", "", "new URL")
	cmd.Flags().StringVar(&geo, "geo", "", "new geographic position (lat;lon)")
	cmd.Flags().StringVar(&rrule, "recurrence-rule", "", "new recurrence rule (e.g. FREQ=WEEKLY;BYDAY=MO)")
	cmd.Flags().StringArrayVar(&exdates, "exception-date-times", nil, "exclude date from recurrence (YYYY-MM-DD, repeatable)")
	cmd.Flags().StringArrayVar(&rdates, "recurrence-date-times", nil, "add extra recurrence date (YYYY-MM-DD, repeatable)")
	cmd.Flags().StringArrayVar(&attachFlags, "attach", nil, "attachment (file path or URL, repeatable)")
	cmd.Flags().StringArrayVar(&alarmFlags, "alarm", nil, `alarm in format [ACTION:]TRIGGER[:DESC:REPEAT:DURATION:RELATED:ATTENDEES]; repeatable`)
	cmd.Flags().StringArrayVar(&attendeeFlags, "attendee", nil, "attendee (email or \"Name <email>\"; repeatable)")
	cmd.Flags().StringVar(&organizer, "organizer", "", "organizer (email or \"Name <email>\")")
	cmd.Flags().StringArrayVar(&commentFlags, "comment", nil, "comment annotation (free-form text, repeatable)")
	cmd.Flags().StringArrayVar(&contactFlags, "contact", nil, "contact info (free-form text, repeatable, replaces all)")
	cmd.Flags().StringArrayVar(&resourceFlags, "resource", nil, "resource needed (e.g. PROJECTOR, repeatable, replaces all)")
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

func todoDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id|uid>",
		Short: "Delete a todo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			t, err := resolveTodo(context.Background(), a, args[0])
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			if err := a.Todos.Delete(context.Background(), t.ID); err != nil {
				return fmt.Errorf("delete todo: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"deleted": true, "id": t.ID})
			}
			fmt.Fprintf(w, "Deleted todo %d.\n", t.ID)
			return nil
		},
	}
	return cmd
}

