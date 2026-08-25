package main

import (
	"context"
	"fmt"
	"io"
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
		noHeader       bool
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
  chroncal journal list --compact   # table with ID, date, categories, and summary`,
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
				writeCompactJournalTable(w, journals, !noHeader, compactTableColorEnabled(w))
				return nil
			}
			printJournals(w, journals)
			return nil
		},
	}
	cmd.Flags().StringVar(&calendarName, "calendar", "", "filter by calendar name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (DRAFT, FINAL, CANCELLED)")
	cmd.Flags().BoolVar(&all, "all", false, "include cancelled entries (hidden by default)")
	cmd.Flags().StringVar(&fromStr, "from", "", "start date (YYYY-MM-DD); with no date flags, past entries are included")
	cmd.Flags().StringVar(&toStr, "to", "", "end date (YYYY-MM-DD, default: 30 days after --from)")
	cmd.Flags().BoolVar(&compact, "compact", false, "table with one line per entry (ID  DATE  CATEGORIES  SUMMARY)")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "omit the compact table header (for scripts)")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted journals (see `journal restore`)")
	return cmd
}

// writeCompactJournalTable renders the journal compact table. The categories
// column disappears when no entry in the result set carries one.
func writeCompactJournalTable(w io.Writer, journals []journal.Journal, showHeader, useColor bool) {
	termWidth := terminalWidth(w)
	headers := []string{"ID", "DATE", "CATEGORIES", "SUMMARY"}
	codes := []string{"1;36", "2", "33", ""}
	rows := make([][]compactCell, len(journals))
	hasCategories := false
	for i, j := range journals {
		categories := textsafe.Display(j.Categories)
		if categories == "" {
			categories = "-"
		} else {
			hasCategories = true
		}
		rows[i] = []compactCell{
			{fmt.Sprintf("%d", j.ID), "1;36"},
			{compactDateColumn(j.StartDate), "2"},
			{categories, "33"},
			{textsafe.Display(j.Summary), ""},
		}
	}
	flex := map[int]bool{2: true, 3: true}
	if !hasCategories {
		headers = dropCompactColumn(headers, 2)
		codes = dropCompactColumn(codes, 2)
		flex = remapFlex(flex, 2, len(headers))
		for i := range rows {
			rows[i] = dropCompactCell(rows[i], 2)
		}
	}
	writeCompactTable(w, headers, codes, rows, flex, useColor, showHeader, termWidth)
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

			parsedExDates, err := parseExdateRdateFlags("exception-date-times", exdates, "", time.Time{})
			if err != nil {
				return err
			}
			parsedRDates, err := parseExdateRdateFlags("recurrence-date-times", rdates, "", time.Time{})
			if err != nil {
				return err
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
				opportunisticPush(a, j.CalendarID, cmd)
				return nil
			}
			printJournal(w, j)
			opportunisticPush(a, j.CalendarID, cmd)
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
	registerRecurrenceAliases(cmd, &rrule, &exdates, &rdates)
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
				parsed, err := parseExdateRdateFlags("exception-date-times", exdates, "", time.Time{})
				if err != nil {
					return err
				}
				p.ExDates = parsed
			}
			if cmd.Flags().Changed("recurrence-date-times") || cmd.Flags().Changed("rdate") {
				parsed, err := parseExdateRdateFlags("recurrence-date-times", rdates, "", time.Time{})
				if err != nil {
					return err
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
				existingAtt, err := a.Journals.ListAttendees(ctx, j.ID)
				if err != nil {
					return fmt.Errorf("load attendees: %w", err)
				}
				attendees := mergeAttendeeUpdate(existingAtt,
					cmd.Flags().Changed("attendee"), parseAttendeeFlags(attendeeFlags),
					cmd.Flags().Changed("organizer"), organizer)
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
				opportunisticPush(a, j.CalendarID, cmd)
				return nil
			}
			printJournal(w, j)
			opportunisticPush(a, j.CalendarID, cmd)
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
	registerRecurrenceAliases(cmd, &rrule, &exdates, &rdates)
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
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
