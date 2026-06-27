package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func todoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "todo",
		Short: "Manage todos",
		Long: `Create, organize, and complete tasks stored in chroncal.

Todos support due dates, start dates, progress, recurrence, alarms, and
the same calendar organization model used by events.`,
		Example: `  chroncal todo list
  chroncal todo add "Ship release" --due 2026-04-15
  chroncal todo complete 7`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		todoListCmd(), todoGetCmd(), todoAddCmd(), todoUpdateCmd(),
		todoDeleteCmd(), todoCompleteCmd(), todoSearchCmd(),
		todoRestoreCmd(), todoPurgeCmd(), todoPurgeDeletedCmd(),
	)
	return cmd
}

func todoListCmd() *cobra.Command {
	var (
		calendarName   string
		status         string
		all            bool
		fromStr        string
		toStr          string
		compact        bool
		includeDeleted bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List todos (incomplete by default)",
		Long: `List todos in a date window.

By default completed and cancelled todos are hidden unless you pass
--all or filter explicitly with --status.`,
		Example: `  chroncal todo list
  chroncal todo list --all
  chroncal todo list --calendar Work --from 2026-04-01 --to 2026-04-30 --output json
  chroncal todo list --compact   # one line per todo (script-friendly)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			from, to, err := parseListDateRange(fromStr, toStr)
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

			todos, err := a.Recurrences.ListFilteredTodos(ctx, recurrence.TodoListParams{
				CalendarID:     calID,
				Status:         status,
				HideCompleted:  !all && status == "",
				From:           from,
				To:             to,
				IncludeDeleted: includeDeleted,
			})
			if err != nil {
				return fmt.Errorf("list todos: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodos(todos))
			}
			if compact {
				if len(todos) == 0 {
					fmt.Fprintln(w, "No todos found.")
					return nil
				}
				for _, t := range todos {
					fmt.Fprintln(w, formatCompactTodo(t))
				}
				return nil
			}
			printTodos(w, todos)
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
	cmd.Flags().BoolVar(&all, "all", false, "include completed and cancelled")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD); with no date flags, overdue todos are included")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 30 days after --from)")
	cmd.Flags().BoolVar(&compact, "compact", false, "one line per todo ([STATUS] DUE  TITLE)")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted todos (see `todo restore`)")
	return cmd
}

// formatCompactTodo renders one todo as a single line for scripting:
// "[x] 2026-05-25  Write report". The checkbox uses [x] when completed,
// [ ] otherwise; the date column is YYYY-MM-DD or "-" (no due date)
// padded to 12 chars so titles line up.
func formatCompactTodo(t todo.Todo) string {
	const dueColWidth = 12
	return fmt.Sprintf("%s %-*s%s", todoCheckbox(t), dueColWidth, compactDateColumn(t.DueDate), textsafe.Display(t.Summary))
}

func todoSearchCmd() *cobra.Command {
	var (
		calendarName string
		status       string
		completed    bool
		incomplete   bool
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search todos by summary, description, location, or categories",
		Long: `Search todos by text fields such as summary, description, location,
and categories.`,
		Example: `  chroncal todo search release
  chroncal todo search invoice --completed
  chroncal todo search onboarding --calendar Work --status IN-PROCESS`,
		Args: exactOneArg,
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

			completedFilter := 0
			if completed {
				completedFilter = 1
			} else if incomplete {
				completedFilter = 2
			}

			todos, err := a.Todos.Search(ctx, todo.SearchParams{
				Query:      args[0],
				CalendarID: calID,
				Status:     status,
				Completed:  completedFilter,
			})
			if err != nil {
				return fmt.Errorf("search todos: %w", err)
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
	cmd.Flags().StringVar(&status, "status", "", "status filter (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
	cmd.Flags().BoolVar(&completed, "completed", false, "show only completed todos")
	cmd.Flags().BoolVar(&incomplete, "incomplete", false, "show only incomplete todos")
	mutuallyExclusive(cmd, "completed", "incomplete")
	return cmd
}

func todoGetCmd() *cobra.Command {
	var recurrenceID string
	cmd := &cobra.Command{
		Use:   "get <id|uid>",
		Short: "Get todo details by ID or UID",
		Long: `Show one todo in detail.

You can look it up by numeric ID or UID. Use --recurrence-id to target a
specific overridden instance from a recurring series.`,
		Example: `  chroncal todo get 7
  chroncal todo get weekly-review-uid
  chroncal todo get weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			t, err := resolveTodo(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			populateTodoFields(ctx, a.Todos, &t)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONTodo(t))
			}
			printTodo(w, t)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
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
					durationVal = durationStr
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

			parsedExDates, err := parseDateFlags(exdates, "", time.Time{})
			if err != nil {
				return fmt.Errorf("--exdate: %w", err)
			}
			parsedRDates, err := parseDateFlags(rdates, "", time.Time{})
			if err != nil {
				return fmt.Errorf("--rdate: %w", err)
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
				pushCalendarAfterWrite(a, t.CalendarID, io.Discard)
				return nil
			}
			printTodo(w, t)
			pushCalendarAfterWrite(a, t.CalendarID, w)
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
		recurrenceID  string
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
  chroncal todo update 1 --summary "Updated task name"

  # Reschedule a todo
  chroncal todo update 1 --due 2026-05-01 --start 2026-04-15

  # Mark as completed
  chroncal todo update 1 --status COMPLETED

  # Switch from due date to estimated duration
  chroncal todo update 1 --due "" --duration 4h

  # Update attendees and add a comment
  chroncal todo update 1 \
    --attendee "Alice <alice@example.com>" \
    --comment "Discussed in standup"

  # Move to a different calendar and change classification
  chroncal todo update 1 --calendar Work --class CONFIDENTIAL

  # Clear the location
  chroncal todo update 1 --location ""`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			existing, err := resolveTodo(ctx, a, args[0], recurrenceID)
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
				Geo:             existing.Geo,
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
					return errInvalidInputf("parse due date: expected YYYY-MM-DD or empty to clear, got %q", dueStr)
				} else {
					p.DueDate = dueStr
				}
			}
			if cmd.Flags().Changed("start") {
				if startStr == "" {
					p.StartDate = ""
				} else if _, err := time.Parse("2006-01-02", startStr); err != nil {
					return errInvalidInputf("parse start date: expected YYYY-MM-DD or empty to clear, got %q", startStr)
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
					return errInvalidInputf("parse duration: %q (use Go format like 1h30m or RFC 5545 like PT1H30M)", durationStr)
				}
			}
			if cmd.Flags().Changed("status") {
				switch strings.ToUpper(status) {
				case "NEEDS-ACTION", "IN-PROCESS", "COMPLETED", "CANCELLED":
				default:
					return errInvalidInputf("invalid --status %q: must be NEEDS-ACTION, IN-PROCESS, COMPLETED, or CANCELLED", status)
				}
				// The service reconciles completed_at and percent_complete
				// with the status (set on completion, cleared on reopen).
				p.Status = strings.ToUpper(status)
			}
			if cmd.Flags().Changed("progress") {
				if progress < 0 || progress > 100 {
					return errInvalidInputf("invalid --progress %d: must be 0-100", progress)
				}
				p.PercentComplete = progress
			}
			if cmd.Flags().Changed("class") {
				switch strings.ToUpper(class) {
				case "PUBLIC", "PRIVATE", "CONFIDENTIAL":
				default:
					return errInvalidInputf("invalid --class %q: must be PUBLIC, PRIVATE, or CONFIDENTIAL", class)
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
					return errInvalidInputf("invalid --priority %d: must be 0-9", priority)
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
				return errInvalidInputf("--due and --duration are mutually exclusive (RFC 5545 §3.6.2)")
			}

			if p.StartDate != "" && p.DueDate != "" && p.StartDate > p.DueDate {
				return errInvalidInputf("--start %s is after --due %s (RFC 5545 §3.6.2: DTSTART must be before DUE)", p.StartDate, p.DueDate)
			}

			if p.Status == "COMPLETED" && cmd.Flags().Changed("progress") && p.PercentComplete != 100 {
				return fmt.Errorf("--status COMPLETED requires 100%% progress, got %d (omit --progress or set it to 100)", p.PercentComplete)
			}

			// Validate parseable flags before updating so a validation
			// failure cannot leave the todo in a partially-updated state.
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

			t, err := a.Todos.Update(ctx, existing.ID, p)
			if err != nil {
				return fmt.Errorf("update todo: %w", err)
			}

			if cmd.Flags().Changed("attach") {
				if err := a.Todos.ReplaceAttachments(ctx, t.ID, attachments); err != nil {
					return fmt.Errorf("update attachments: %w", err)
				}
			}
			if cmd.Flags().Changed("alarm") {
				if err := a.Todos.ReplaceAlarms(ctx, t.ID, alarms); err != nil {
					return fmt.Errorf("update alarms: %w", err)
				}
			}
			if cmd.Flags().Changed("attendee") || cmd.Flags().Changed("organizer") {
				existingAtt, err := a.Todos.ListAttendees(ctx, t.ID)
				if err != nil {
					return fmt.Errorf("load attendees: %w", err)
				}
				attendees := mergeAttendeeUpdate(existingAtt,
					cmd.Flags().Changed("attendee"), parseAttendeeFlags(attendeeFlags),
					cmd.Flags().Changed("organizer"), organizer)
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
			populateTodoFields(ctx, a.Todos, &t)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, toJSONTodo(t)); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, t.CalendarID, io.Discard)
				return nil
			}
			printTodo(w, t)
			pushCalendarAfterWrite(a, t.CalendarID, w)
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
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}

func todoCompleteCmd() *cobra.Command {
	var recurrenceID string
	cmd := &cobra.Command{
		Use:   "complete <id|uid>",
		Short: "Mark a todo as completed",
		Long: `Mark a todo as completed, set its completion timestamp, and update
its progress to 100%.`,
		Example: `  chroncal todo complete 7
  chroncal todo complete weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			t, err := resolveTodo(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			t, err = a.Todos.Complete(ctx, t.ID)
			if err != nil {
				return fmt.Errorf("complete todo: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, toJSONTodo(t)); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, t.CalendarID, io.Discard)
				return nil
			}
			fmt.Fprintf(w, "Completed: %s\n", safeText(t.Summary))
			pushCalendarAfterWrite(a, t.CalendarID, w)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}

func todoDeleteCmd() *cobra.Command {
	var (
		recurrenceID string
		series       bool
	)
	cmd := &cobra.Command{
		Use:   "delete <id|uid>",
		Short: "Delete a todo",
		Long: `Delete a single todo, a specific recurring override, or an entire
recurring series.`,
		Example: `  chroncal todo delete 7
  chroncal todo delete weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z
  chroncal todo delete weekly-review-uid --series`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			t, err := resolveTodo(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}

			if series && recurrenceID != "" {
				return errInvalidInputf("--series and --recurrence-id are mutually exclusive")
			}

			question := fmt.Sprintf("Delete todo %q?", safeText(t.Summary))
			if series {
				question = fmt.Sprintf("Delete the entire recurring series %q (master + all overrides)?", safeText(t.Summary))
			} else if recurrenceID != "" {
				question = fmt.Sprintf("Delete override instance of %q at %s?", safeText(t.Summary), recurrenceID)
			}
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if series {
				if err := a.Todos.DeleteSeries(ctx, t.UID); err != nil {
					return fmt.Errorf("delete series: %w", err)
				}
				w := cmd.OutOrStdout()
				if outputFmt != "text" {
					if err := printOutput(w, map[string]any{"deleted": true, "uid": t.UID, "series": true}); err != nil {
						return err
					}
					pushCalendarAfterWrite(a, t.CalendarID, io.Discard)
					return nil
				}
				fmt.Fprintf(w, "Deleted todo series %q.\n", safeText(t.UID))
				pushCalendarAfterWrite(a, t.CalendarID, w)
				return nil
			}

			if err := a.Todos.Delete(ctx, t.ID); err != nil {
				return fmt.Errorf("delete todo: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, map[string]any{"deleted": true, "id": t.ID}); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, t.CalendarID, io.Discard)
				return nil
			}
			fmt.Fprintf(w, "Deleted todo %d.\n", t.ID)
			pushCalendarAfterWrite(a, t.CalendarID, w)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	cmd.Flags().BoolVar(&series, "series", false, "delete the entire recurring series (master + all overrides)")
	addConfirmFlag(cmd)
	return cmd
}

func todoRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <id-or-uid>",
		Short: "Restore a soft-deleted todo",
		Long: `Restore clears the deletion marker on a soft-deleted todo so it
reappears in list and TUI views.

The todo must have been deleted via chroncal (soft-delete, not purged).

If the todo was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely (with a fresh resource URL).`,
		Example: `  chroncal todo restore 7
  chroncal todo restore weekly-review-uid`,
		Args: exactOneArg,
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
				if err := a.Todos.RestoreByID(ctx, id); err != nil {
					if errors.Is(err, todo.ErrNotDeleted) {
						return fmt.Errorf("todo %d not found (may have been purged)", id)
					}
					return fmt.Errorf("restore todo: %w", err)
				}
				if outputFmt != "text" {
					return printOutput(w, map[string]any{"restored": true, "id": id})
				}
				fmt.Fprintf(w, "Restored todo %d.\n", id)
				return nil
			}

			if err := a.Todos.RestoreByUID(ctx, ref); err != nil {
				if errors.Is(err, todo.ErrNotDeleted) {
					return fmt.Errorf("todo %q not found (may have been purged)", ref)
				}
				return fmt.Errorf("restore todo: %w", err)
			}
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"restored": true, "uid": ref})
			}
			fmt.Fprintf(w, "Restored todo(s) with uid %q.\n", safeText(ref))
			return nil
		},
	}
	return cmd
}

func todoPurgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "purge <id>",
		Short: "Hard-delete a single soft-deleted todo",
		Long: `Purge permanently removes one soft-deleted todo from the database.

The todo must already be soft-deleted. Purging a live todo is refused;
use 'todo delete' first. Purging is not reversible — child rows cascade.`,
		Example: `  chroncal todo purge 7
  chroncal todo purge 7 --yes`,
		Args: exactOneArg,
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

			td, err := a.Todos.GetIncludingDeleted(ctx, id)
			if err != nil {
				return fmt.Errorf("get todo: %w", err)
			}
			if td.DeletedAt == nil {
				return fmt.Errorf("todo %d is live; run 'todo delete %d' first", id, id)
			}

			question := fmt.Sprintf("Purge todo %q (id %d)? This cannot be undone.", safeText(td.Summary), id)
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if err := a.Todos.PurgeByID(ctx, id); err != nil {
				if errors.Is(err, todo.ErrNotDeleted) {
					return fmt.Errorf("todo %d not found or not soft-deleted", id)
				}
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": true, "id": id})
			}
			fmt.Fprintf(w, "Purged todo %d.\n", id)
			return nil
		},
	}
	addConfirmFlag(cmd)
	return cmd
}

func todoPurgeDeletedCmd() *cobra.Command {
	var olderThanStr string
	cmd := &cobra.Command{
		Use:   "purge-deleted",
		Short: "Hard-delete soft-deleted todos older than --older-than",
		Long: `Purge permanently removes soft-deleted todos from the database.

By default, only todos soft-deleted more than 30 days ago are purged.
Use --older-than to pick a different age (e.g. 7d, 24h, 720h).

This operation is destructive and not reversible. Attachments and other
child rows cascade.`,
		Example: `  chroncal todo purge-deleted                   # 30 days by default
  chroncal todo purge-deleted --older-than 7d   # older than a week
  chroncal todo purge-deleted --older-than 0s --yes  # purge everything soft-deleted`,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseCLIDuration("older-than", olderThanStr)
			if err != nil {
				return err
			}
			if d < 0 {
				return errInvalidInputf("--older-than must be non-negative, got %s", d)
			}
			if d < time.Hour {
				prompt := fmt.Sprintf("Purge ALL todos soft-deleted in the last %s? This cannot be undone.", d)
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
			n, err := a.Todos.PurgeDeleted(ctx, cutoff)
			if err != nil {
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": n, "older_than": d.String()})
			}
			fmt.Fprintf(w, "Purged %d todo(s) soft-deleted more than %s ago.\n", n, d)
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThanStr, "older-than", "720h", "age threshold (Go duration, e.g. 30d=720h, 168h=7 days)")
	addConfirmFlag(cmd)
	return cmd
}
