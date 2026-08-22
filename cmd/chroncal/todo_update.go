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
		clearForeign  bool
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
					// Store the canonical upper case; see the create
					// command for the reason.
					p.Duration = strings.ToUpper(durationStr)
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
			switch {
			case cmd.Flags().Changed("alarm") && clearForeign:
				// The flag makes the replacement set the whole set, so a
				// preserved alarm goes with the rows the flags replace.
				if err := a.Todos.ReplaceAlarms(ctx, t.ID, alarms); err != nil {
					return fmt.Errorf("update alarms: %w", err)
				}
			case cmd.Flags().Changed("alarm"):
				if err := a.Todos.ReplaceFireableAlarms(ctx, t.ID, alarms); err != nil {
					return fmt.Errorf("update alarms: %w", err)
				}
			case clearForeign:
				// The flag on its own removes the preserved rows and keeps
				// the alarms the user can state.
				if err := a.Todos.ClearSyncOnlyAlarms(ctx, t.ID); err != nil {
					return fmt.Errorf("clear foreign alarms: %w", err)
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
				opportunisticPush(a, t.CalendarID, cmd)
				return nil
			}
			printTodo(w, t)
			opportunisticPush(a, t.CalendarID, cmd)
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
	cmd.Flags().StringArrayVar(&alarmFlags, "alarm", nil, alarmFlagHelp)
	cmd.Flags().BoolVar(&clearForeign, "clear-foreign-alarms", false, clearForeignAlarmsHelp)
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
