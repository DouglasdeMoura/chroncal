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

	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

func journalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "Manage journal entries",
		Long: `Create and manage journal entries such as notes, logs, and dated
records.

Journal entries can be recurring and can carry categories, attachments,
contacts, attendees, and related-item metadata.`,
		Example: `  chroncal journal list
  chroncal journal add "Sprint retro" --date 2026-04-01
  chroncal journal search retro`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		journalListCmd(), journalGetCmd(), journalAddCmd(), journalUpdateCmd(),
		journalDeleteCmd(), journalSearchCmd(),
		journalRestoreCmd(), journalPurgeCmd(), journalPurgeDeletedCmd(),
	)
	return cmd
}

func journalListCmd() *cobra.Command {
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
		Short: "List journal entries",
		Long: `List journal entries in a date window.

CANCELLED entries are hidden by default. Pass --all to include them, or
filter explicitly with --status to narrow the list to DRAFT, FINAL, or
CANCELLED entries.`,
		Example: `  chroncal journal list
  chroncal journal list --calendar Work --from 2026-04-01 --to 2026-04-30
  chroncal journal list --status DRAFT --output json
  chroncal journal list --compact   # one line per entry (script-friendly)`,
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

			journals, err := a.Recurrences.ListFilteredJournals(ctx, recurrence.JournalListParams{
				CalendarID:     calID,
				Status:         status,
				HideCancelled:  !all && status == "",
				From:           from,
				To:             to,
				IncludeDeleted: includeDeleted,
			})
			if err != nil {
				return fmt.Errorf("list journals: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONJournals(journals))
			}
			if compact {
				if len(journals) == 0 {
					fmt.Fprintln(w, "No journal entries found.")
					return nil
				}
				for _, j := range journals {
					fmt.Fprintln(w, formatCompactJournal(j))
				}
				return nil
			}
			printJournals(w, journals)
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (DRAFT, FINAL, CANCELLED)")
	cmd.Flags().BoolVar(&all, "all", false, "include cancelled entries (hidden by default)")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD, default: today)")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 30 days from now)")
	cmd.Flags().BoolVar(&compact, "compact", false, "one line per entry (DATE  SUMMARY)")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted journals (see `journal restore`)")
	return cmd
}

// formatCompactJournal renders one journal entry as a single line:
// "2026-05-15  Notes". Entries without a start date show "-" in the
// date column (12-char width).
func formatCompactJournal(j journal.Journal) string {
	const dateColWidth = 12
	return fmt.Sprintf("%-*s%s", dateColWidth, compactDateColumn(j.StartDate), textsafe.Display(j.Summary))
}

func journalGetCmd() *cobra.Command {
	var recurrenceID string
	cmd := &cobra.Command{
		Use:   "get <id|uid>",
		Short: "Get journal entry details by ID or UID",
		Long: `Show one journal entry in detail.

You can look it up by numeric ID or UID. Use --recurrence-id to target a
specific overridden instance from a recurring series.`,
		Example: `  chroncal journal get 12
  chroncal journal get weekly-review-uid
  chroncal journal get weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			j, err := resolveJournal(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get journal: %w", err)
			}

			populateJournalFields(ctx, a.Journals, &j)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONJournal(j))
			}
			printJournal(w, j)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	return cmd
}

func journalAddCmd() *cobra.Command {
	var (
		description   string
		dateStr       string
		calendarName  string
		status        string
		class         string
		categories    string
		url           string
		rrule         string
		exdates       []string
		rdates        []string
		attachFlags   []string
		attendeeFlags []string
		commentFlags  []string
		contactFlags  []string
		relationFlags []string
		organizer     string
	)
	cmd := &cobra.Command{
		Use:   `add "<summary>"`,
		Short: "Create a new journal entry",
		Long: `Create a new journal entry in the calendar.

Date is date-only (YYYY-MM-DD) and stored without a time component,
so it exports correctly as VALUE=DATE in iCal regardless of your timezone.

Defaults: status=FINAL, class=PUBLIC, calendar=Personal.`,
		Example: `  # Simple journal entry
  chroncal journal add "Meeting notes"

  # Journal with date and description
  chroncal journal add "Sprint retrospective" --date 2026-04-01 \
    --description "Discussed velocity improvements"

  # Draft journal with categories
  chroncal journal add "Research notes" --status DRAFT \
    --categories "research,ai" --class PRIVATE

  # Recurring weekly journal
  chroncal journal add "Weekly review" --date 2026-04-01 \
    --rrule "FREQ=WEEKLY;BYDAY=FR"`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			if strings.TrimSpace(args[0]) == "" {
				return errInvalidInputf("journal summary must not be empty")
			}

			calID, err := resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}

			// Validate enums
			if status != "" {
				switch strings.ToUpper(status) {
				case "DRAFT", "FINAL", "CANCELLED":
				default:
					return errInvalidInputf("invalid --status %q: must be DRAFT, FINAL, or CANCELLED", status)
				}
			}
			if class != "" {
				switch strings.ToUpper(class) {
				case "PUBLIC", "PRIVATE", "CONFIDENTIAL":
				default:
					return errInvalidInputf("invalid --class %q: must be PUBLIC, PRIVATE, or CONFIDENTIAL", class)
				}
			}
			if err := validateRRule(rrule); err != nil {
				return err
			}
			if err := validateURL(url); err != nil {
				return err
			}

			var startDate string
			if dateStr != "" {
				if _, err := time.Parse("2006-01-02", dateStr); err != nil {
					return errInvalidInputf("parse date: expected YYYY-MM-DD, got %q", dateStr)
				}
				startDate = dateStr
			}

			parsedExDates, err := parseDateFlags(exdates, "", time.Time{})
			if err != nil {
				return fmt.Errorf("--exdate: %w", err)
			}
			parsedRDates, err := parseDateFlags(rdates, "", time.Time{})
			if err != nil {
				return fmt.Errorf("--rdate: %w", err)
			}

			// Validate all parseable flags before creating the journal so a
			// validation failure cannot leave an orphaned row in the database.
			var attachments []model.Attachment
			if len(attachFlags) > 0 {
				attachments, err = parseAttachFlags(attachFlags)
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

			j, err := a.Journals.Create(ctx, journal.CreateParams{
				CalendarID:     calID,
				Summary:        args[0],
				Description:    description,
				StartDate:      startDate,
				Status:         strings.ToUpper(status),
				Class:          strings.ToUpper(class),
				Categories:     categories,
				URL:            url,
				RecurrenceRule: rrule,
				ExDates:        parsedExDates,
				RDates:         parsedRDates,
			})
			if err != nil {
				return fmt.Errorf("create journal: %w", err)
			}

			if len(attachments) > 0 {
				if err := a.Journals.ReplaceAttachments(ctx, j.ID, attachments); err != nil {
					return fmt.Errorf("add attachments: %w", err)
				}
			}
			if len(attendeeFlags) > 0 || organizer != "" {
				attendees := parseAttendeeFlags(attendeeFlags)
				if organizer != "" {
					attendees = append(attendees, parseOrganizerFlag(organizer))
				}
				if err := a.Journals.ReplaceAttendees(ctx, j.ID, attendees); err != nil {
					return fmt.Errorf("add attendees: %w", err)
				}
			}
			if len(commentFlags) > 0 {
				if err := a.Journals.ReplaceComments(ctx, j.ID, commentFlags); err != nil {
					return fmt.Errorf("add comments: %w", err)
				}
			}
			if len(contactFlags) > 0 {
				if err := a.Journals.ReplaceContacts(ctx, j.ID, contactFlags); err != nil {
					return fmt.Errorf("add contacts: %w", err)
				}
			}
			if len(relations) > 0 {
				if err := a.Journals.ReplaceRelations(ctx, j.ID, relations); err != nil {
					return fmt.Errorf("add relations: %w", err)
				}
			}

			// Re-read related data for output
			populateJournalFields(ctx, a.Journals, &j)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, toJSONJournal(j)); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, j.CalendarID, io.Discard)
				return nil
			}
			printJournal(w, j)
			pushCalendarAfterWrite(a, j.CalendarID, w)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "description")
	cmd.Flags().StringVar(&dateStr, "date", "", "date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "calendar name (default: first available)")
	cmd.Flags().StringVar(&status, "status", "", "status (DRAFT, FINAL, CANCELLED; default: FINAL)")
	cmd.Flags().StringVar(&class, "class", "", "classification (PUBLIC, PRIVATE, CONFIDENTIAL; default: PUBLIC)")
	cmd.Flags().StringVar(&categories, "categories", "", "comma-separated categories")
	cmd.Flags().StringVar(&url, "url", "", "associated URL")
	cmd.Flags().StringVar(&rrule, "recurrence-rule", "", "RFC 5545 recurrence rule (e.g. FREQ=WEEKLY;BYDAY=FR)")
	cmd.Flags().StringArrayVar(&exdates, "exception-date-times", nil, "exclude date from recurrence (YYYY-MM-DD, repeatable)")
	cmd.Flags().StringArrayVar(&rdates, "recurrence-date-times", nil, "add extra recurrence date (YYYY-MM-DD, repeatable)")
	cmd.Flags().StringArrayVar(&attachFlags, "attach", nil, "attachment (file path or URL, repeatable)")
	cmd.Flags().StringArrayVar(&attendeeFlags, "attendee", nil, "attendee (email or \"Name <email>\"; repeatable)")
	cmd.Flags().StringVar(&organizer, "organizer", "", "organizer (email or \"Name <email>\")")
	cmd.Flags().StringArrayVar(&commentFlags, "comment", nil, "comment annotation (free-form text, repeatable)")
	cmd.Flags().StringArrayVar(&contactFlags, "contact", nil, "contact info (free-form text, repeatable)")
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

func journalUpdateCmd() *cobra.Command {
	var (
		summary       string
		description   string
		dateStr       string
		status        string
		calendarName  string
		class         string
		categories    string
		url           string
		rrule         string
		exdates       []string
		rdates        []string
		attachFlags   []string
		attendeeFlags []string
		commentFlags  []string
		contactFlags  []string
		relationFlags []string
		organizer     string
		recurrenceID  string
	)
	cmd := &cobra.Command{
		Use:   "update <id|uid>",
		Short: "Update an existing journal entry",
		Long: `Update an existing journal entry by numeric ID or UID.

Only the flags you pass are changed; all other fields keep their current
values. Use an empty string to clear optional fields like --date,
--description, --url, --categories, or --rrule.

Repeatable flags (--attendee, --comment, --contact, --attach,
--related-to) replace all existing values when specified.`,
		Example: `  # Change the summary
  chroncal journal update 1 --summary "Updated notes"

  # Change the date
  chroncal journal update 1 --date 2026-05-01

  # Mark as draft
  chroncal journal update 1 --status DRAFT

  # Move to a different calendar
  chroncal journal update 1 --calendar Work`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			existing, err := resolveJournal(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get journal: %w", err)
			}

			p := journal.UpdateParams{
				Summary:        existing.Summary,
				Description:    existing.Description,
				StartDate:      existing.StartDate,
				Status:         existing.Status,
				CalendarID:     existing.CalendarID,
				Class:          existing.Class,
				URL:            existing.URL,
				Categories:     existing.Categories,
				RecurrenceRule: existing.RecurrenceRule,
				Timezone:       existing.Timezone,
				ExDates:        existing.ExDates,
				RDates:         existing.RDates,
			}

			if cmd.Flags().Changed("summary") {
				p.Summary = summary
			}
			if cmd.Flags().Changed("description") {
				p.Description = description
			}
			if cmd.Flags().Changed("date") {
				if dateStr == "" {
					p.StartDate = ""
				} else if _, err := time.Parse("2006-01-02", dateStr); err != nil {
					return errInvalidInputf("parse date: expected YYYY-MM-DD or empty to clear, got %q", dateStr)
				} else {
					p.StartDate = dateStr
				}
			}
			if cmd.Flags().Changed("status") {
				switch strings.ToUpper(status) {
				case "DRAFT", "FINAL", "CANCELLED":
				default:
					return errInvalidInputf("invalid --status %q: must be DRAFT, FINAL, or CANCELLED", status)
				}
				p.Status = strings.ToUpper(status)
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
			if cmd.Flags().Changed("categories") {
				p.Categories = categories
			}
			if cmd.Flags().Changed("url") {
				if err := validateURL(url); err != nil {
					return err
				}
				p.URL = url
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

			// Validate parseable flags before updating.
			var attachments []model.Attachment
			if cmd.Flags().Changed("attach") {
				attachments, err = parseAttachFlags(attachFlags)
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

			j, err := a.Journals.Update(ctx, existing.ID, p)
			if err != nil {
				return fmt.Errorf("update journal: %w", err)
			}

			if cmd.Flags().Changed("attach") {
				if err := a.Journals.ReplaceAttachments(ctx, j.ID, attachments); err != nil {
					return fmt.Errorf("update attachments: %w", err)
				}
			}
			if cmd.Flags().Changed("attendee") || cmd.Flags().Changed("organizer") {
				attendees := parseAttendeeFlags(attendeeFlags)
				if organizer != "" {
					attendees = append(attendees, parseOrganizerFlag(organizer))
				}
				if err := a.Journals.ReplaceAttendees(ctx, j.ID, attendees); err != nil {
					return fmt.Errorf("update attendees: %w", err)
				}
			}
			if cmd.Flags().Changed("comment") {
				if err := a.Journals.ReplaceComments(ctx, j.ID, commentFlags); err != nil {
					return fmt.Errorf("update comments: %w", err)
				}
			}
			if cmd.Flags().Changed("contact") {
				if err := a.Journals.ReplaceContacts(ctx, j.ID, contactFlags); err != nil {
					return fmt.Errorf("update contacts: %w", err)
				}
			}
			if cmd.Flags().Changed("related-to") {
				if err := a.Journals.ReplaceRelations(ctx, j.ID, relations); err != nil {
					return fmt.Errorf("update relations: %w", err)
				}
			}

			// Re-read related data for output
			populateJournalFields(ctx, a.Journals, &j)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, toJSONJournal(j)); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, j.CalendarID, io.Discard)
				return nil
			}
			printJournal(w, j)
			pushCalendarAfterWrite(a, j.CalendarID, w)
			return nil
		},
	}
	cmd.Flags().StringVar(&summary, "summary", "", "new summary")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&dateStr, "date", "", "new date (YYYY-MM-DD; empty to clear)")
	cmd.Flags().StringVar(&status, "status", "", "new status (DRAFT, FINAL, CANCELLED)")
	cmd.Flags().StringVar(&class, "class", "", "new classification (PUBLIC, PRIVATE, CONFIDENTIAL)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "move to calendar")
	cmd.Flags().StringVar(&categories, "categories", "", "new categories")
	cmd.Flags().StringVar(&url, "url", "", "new URL")
	cmd.Flags().StringVar(&rrule, "recurrence-rule", "", "new recurrence rule (e.g. FREQ=WEEKLY;BYDAY=FR)")
	cmd.Flags().StringArrayVar(&exdates, "exception-date-times", nil, "exclude date from recurrence (YYYY-MM-DD, repeatable)")
	cmd.Flags().StringArrayVar(&rdates, "recurrence-date-times", nil, "add extra recurrence date (YYYY-MM-DD, repeatable)")
	cmd.Flags().StringArrayVar(&attachFlags, "attach", nil, "attachment (file path or URL, repeatable)")
	cmd.Flags().StringArrayVar(&attendeeFlags, "attendee", nil, "attendee (email or \"Name <email>\"; repeatable)")
	cmd.Flags().StringVar(&organizer, "organizer", "", "organizer (email or \"Name <email>\")")
	cmd.Flags().StringArrayVar(&commentFlags, "comment", nil, "comment annotation (free-form text, repeatable)")
	cmd.Flags().StringArrayVar(&contactFlags, "contact", nil, "contact info (free-form text, repeatable, replaces all)")
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

func journalDeleteCmd() *cobra.Command {
	var (
		recurrenceID string
		series       bool
	)
	cmd := &cobra.Command{
		Use:   "delete <id|uid>",
		Short: "Delete a journal entry",
		Long: `Delete a single journal entry, a specific recurring override, or an
entire recurring series.`,
		Example: `  chroncal journal delete 12
  chroncal journal delete weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z
  chroncal journal delete weekly-review-uid --series`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			j, err := resolveJournal(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get journal: %w", err)
			}

			if series && recurrenceID != "" {
				return errInvalidInputf("--series and --recurrence-id are mutually exclusive")
			}

			question := fmt.Sprintf("Delete journal %q?", safeText(j.Summary))
			if series {
				question = fmt.Sprintf("Delete the entire recurring series %q (master + all overrides)?", safeText(j.Summary))
			} else if recurrenceID != "" {
				question = fmt.Sprintf("Delete override instance of %q at %s?", safeText(j.Summary), recurrenceID)
			}
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if series {
				if err := a.Journals.DeleteSeries(ctx, j.UID); err != nil {
					return fmt.Errorf("delete series: %w", err)
				}
				w := cmd.OutOrStdout()
				if outputFmt != "text" {
					if err := printOutput(w, map[string]any{"deleted": true, "uid": j.UID, "series": true}); err != nil {
						return err
					}
					pushCalendarAfterWrite(a, j.CalendarID, io.Discard)
					return nil
				}
				fmt.Fprintf(w, "Deleted journal series %q.\n", safeText(j.UID))
				pushCalendarAfterWrite(a, j.CalendarID, w)
				return nil
			}

			if err := a.Journals.Delete(ctx, j.ID); err != nil {
				return fmt.Errorf("delete journal: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				if err := printOutput(w, map[string]any{"deleted": true, "id": j.ID}); err != nil {
					return err
				}
				pushCalendarAfterWrite(a, j.CalendarID, io.Discard)
				return nil
			}
			fmt.Fprintf(w, "Deleted journal %d.\n", j.ID)
			pushCalendarAfterWrite(a, j.CalendarID, w)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	cmd.Flags().BoolVar(&series, "series", false, "delete the entire recurring series (master + all overrides)")
	addConfirmFlag(cmd)
	return cmd
}

func journalRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <id-or-uid>",
		Short: "Restore a soft-deleted journal entry",
		Long: `Restore clears the deletion marker on a soft-deleted journal entry
so it reappears in list and TUI views.

If the journal was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely.`,
		Example: `  chroncal journal restore 3
  chroncal journal restore retro-uid`,
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
				if err := a.Journals.RestoreByID(ctx, id); err != nil {
					if errors.Is(err, journal.ErrNotDeleted) {
						return fmt.Errorf("journal %d not found (may have been purged)", id)
					}
					return fmt.Errorf("restore journal: %w", err)
				}
				if outputFmt != "text" {
					return printOutput(w, map[string]any{"restored": true, "id": id})
				}
				fmt.Fprintf(w, "Restored journal %d.\n", id)
				return nil
			}

			if err := a.Journals.RestoreByUID(ctx, ref); err != nil {
				return fmt.Errorf("restore journal: %w", err)
			}
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"restored": true, "uid": ref})
			}
			fmt.Fprintf(w, "Restored journal(s) with uid %q.\n", safeText(ref))
			return nil
		},
	}
	return cmd
}

func journalPurgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "purge <id>",
		Short: "Hard-delete a single soft-deleted journal entry",
		Long: `Purge permanently removes one soft-deleted journal entry from the
database.

The entry must already be soft-deleted. Purging a live entry is refused;
use 'journal delete' first. Purging is not reversible — child rows cascade.`,
		Example: `  chroncal journal purge 3
  chroncal journal purge 3 --yes`,
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

			j, err := a.Journals.GetIncludingDeleted(ctx, id)
			if err != nil {
				return fmt.Errorf("get journal: %w", err)
			}
			if j.DeletedAt == nil {
				return fmt.Errorf("journal %d is live; run 'journal delete %d' first", id, id)
			}

			question := fmt.Sprintf("Purge journal %q (id %d)? This cannot be undone.", safeText(j.Summary), id)
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if err := a.Journals.PurgeByID(ctx, id); err != nil {
				if errors.Is(err, journal.ErrNotDeleted) {
					return fmt.Errorf("journal %d not found or not soft-deleted", id)
				}
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": true, "id": id})
			}
			fmt.Fprintf(w, "Purged journal %d.\n", id)
			return nil
		},
	}
	addConfirmFlag(cmd)
	return cmd
}

func journalPurgeDeletedCmd() *cobra.Command {
	var olderThanStr string
	cmd := &cobra.Command{
		Use:   "purge-deleted",
		Short: "Hard-delete soft-deleted journals older than --older-than",
		Long: `Purge permanently removes soft-deleted journal entries from the
database. By default only rows soft-deleted more than 30 days ago are
purged. Use --older-than to pick a different age.

This operation is destructive and not reversible. Attachments and other
child rows cascade.`,
		Example: `  chroncal journal purge-deleted
  chroncal journal purge-deleted --older-than 7d
  chroncal journal purge-deleted --older-than 0s --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseCLIDuration("older-than", olderThanStr)
			if err != nil {
				return err
			}
			if d < 0 {
				return errInvalidInputf("--older-than must be non-negative, got %s", d)
			}
			if d < time.Hour {
				prompt := fmt.Sprintf("Purge ALL journals soft-deleted in the last %s? This cannot be undone.", d)
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
			n, err := a.Journals.PurgeDeleted(ctx, cutoff)
			if err != nil {
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": n, "older_than": d.String()})
			}
			fmt.Fprintf(w, "Purged %d journal(s) soft-deleted more than %s ago.\n", n, d)
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThanStr, "older-than", "720h", "age threshold (Go duration, e.g. 30d=720h, 168h=7 days)")
	addConfirmFlag(cmd)
	return cmd
}

func journalSearchCmd() *cobra.Command {
	var (
		calendarName string
		status       string
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search journal entries by summary, description, or categories",
		Long: `Search journal entries by text fields such as summary,
description, and categories.`,
		Example: `  chroncal journal search retro
  chroncal journal search architecture --calendar Work
  chroncal journal search research --status DRAFT --output json`,
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

			journals, err := a.Journals.Search(ctx, journal.SearchParams{
				Query:      args[0],
				CalendarID: calID,
				Status:     status,
			})
			if err != nil {
				return fmt.Errorf("search journals: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONJournals(journals))
			}
			printJournals(w, journals)
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "status filter (DRAFT, FINAL, CANCELLED)")
	return cmd
}
