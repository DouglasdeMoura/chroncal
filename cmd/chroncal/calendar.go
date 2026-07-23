package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/douglasdemoura/chroncal/internal/app"
	calendarpkg "github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

func calendarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "calendar",
		Aliases: []string{"cal"},
		Short:   "Manage local calendars",
		Long: `Create and organize local calendars.

Calendars can stay local-only, or they can be linked to a remote CalDAV
calendar for sync.`,
		Example: `  chroncal calendar list
  chroncal calendar create "Work"
  chroncal calendar create "Work" --remote-url https://cal.example.com/dav/calendars/work/ --username alice --auth bearer
  chroncal calendar update Work --remote-url https://cal.example.com/dav/calendars/work/ --username alice --auth bearer`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		calendarListCmd(),
		calendarGetCmd(),
		calendarCreateCmd(),
		calendarUpdateCmd(),
		calendarDeleteCmd(),
		calendarSetDefaultCmd(),
	)
	return cmd
}

func calendarSetDefaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-default <id|name>",
		Short: "Mark a calendar as the default",
		Long: `Promote a calendar to the default. New events, todos, and journals
without an explicit --calendar use the default.

Exactly one calendar is default at any time.`,
		Example: `  chroncal calendar set-default Work
  chroncal calendar set-default 2`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			cals, err := a.Calendars.List(ctx)
			if err != nil {
				return fmt.Errorf("list calendars: %w", err)
			}
			target, err := findCalendarByRef(cals, args[0])
			if err != nil {
				return err
			}

			if target.IsDefault {
				w := cmd.OutOrStdout()
				if outputFmt != "text" {
					return printOutput(w, toJSONCalendar(target))
				}
				fmt.Fprintf(w, "Calendar %q is already the default.\n", target.Name)
				return nil
			}

			if err := a.Calendars.SetDefault(ctx, target.ID); err != nil {
				return fmt.Errorf("set default: %w", err)
			}

			updated, err := a.Calendars.Get(ctx, target.ID)
			if err != nil {
				return fmt.Errorf("get calendar: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONCalendar(updated))
			}
			fmt.Fprintf(w, "Default calendar set to %q.\n", updated.Name)
			return nil
		},
	}
	return cmd
}

func calendarListCmd() *cobra.Command {
	var compact bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all calendars",
		Long:  `Show the local calendars available in your chroncal database.`,
		Example: `  chroncal calendar list
  chroncal calendar list --output json
  chroncal calendar list --compact   # one line per calendar (script-friendly)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			cals, err := a.Calendars.List(context.Background())
			if err != nil {
				return fmt.Errorf("list calendars: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				items := make([]jsonCalendar, len(cals))
				for i, c := range cals {
					items[i] = toJSONCalendar(c)
				}
				return printOutput(w, items)
			}
			if compact {
				if len(cals) == 0 {
					fmt.Fprintln(w, "No calendars found.")
					return nil
				}
				for _, c := range cals {
					fmt.Fprintln(w, formatCompactCalendar(c))
				}
				return nil
			}
			printCalendars(w, cals)
			return nil
		},
	}
	cmd.Flags().BoolVar(&compact, "compact", false, "one line per calendar (NAME  COLOR)")
	return cmd
}

// formatCompactCalendar renders one calendar as a single line:
// "Personal  #7C3AED". A leading "* " marks the default so a glance at
// the list is enough to see which calendar new events land in. Name is
// padded to a fixed column width so the color column lines up across rows.
func formatCompactCalendar(c calendarpkg.Calendar) string {
	const nameColWidth = 20
	prefix := "  "
	if c.IsDefault {
		prefix = "* "
	}
	return fmt.Sprintf("%s%-*s%s", prefix, nameColWidth, textsafe.Display(c.Name), c.Color)
}

func calendarGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get calendar details by ID",
		Long:  `Show one calendar, including its metadata and sync link details.`,
		Example: `  chroncal calendar get 1
  chroncal calendar get 1 --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return errInvalidInputf("invalid calendar ID: %v", err)
			}

			c, err := a.Calendars.Get(context.Background(), id)
			if err != nil {
				return notFoundErr(err, "calendar", id)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONCalendar(c))
			}
			printCalendar(w, c)
			return nil
		},
	}
	return cmd
}

func calendarCreateCmd() *cobra.Command {
	var (
		color         string
		description   string
		email         string
		remoteURL     string
		username      string
		authType      string
		oauthClientID string
		allowInsecure bool
	)
	cmd := &cobra.Command{
		Use:   `create "<name>"`,
		Short: "Create a new calendar",
		Long: `Create a local calendar for events, todos, and journal entries.

The default color is only a presentation hint; it does not affect sync
behavior.`,
		Example: `  chroncal calendar create "Work"
  chroncal calendar create "Family" --color "#0F766E" --description "Shared family schedule"
  chroncal calendar create "Work" --remote-url https://cal.example.com/dav/calendars/work/ --username alice --auth bearer`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errInvalidInputf("calendar name must not be empty")
			}
			if err := validateCalendarRemoteFlags(remoteURL, username, authType, oauthClientID, allowInsecure, false); err != nil {
				return err
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			c, err := a.Calendars.Create(context.Background(), args[0], color, description)
			if err != nil {
				return fmt.Errorf("create calendar: %w", err)
			}

			if email != "" {
				if err := a.Calendars.SetOwnerEmail(context.Background(), c.ID, email); err != nil {
					return fmt.Errorf("set owner email: %w", err)
				}
			}

			if strings.TrimSpace(remoteURL) != "" {
				if err := connectCalendarRemote(cmd.Context(), a, c, calendarRemoteFlags{
					RemoteURL:     remoteURL,
					Username:      username,
					AuthType:      authType,
					OAuthClientID: oauthClientID,
					AllowInsecure: allowInsecure,
				}); err != nil {
					return err
				}

				c, err = a.Calendars.Get(context.Background(), c.ID)
				if err != nil {
					return fmt.Errorf("get calendar: %w", err)
				}
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONCalendar(c))
			}
			fmt.Fprintf(w, "Created calendar %d: %s\n", c.ID, c.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&color, "color", "#7C3AED", "calendar color (hex)")
	cmd.Flags().StringVar(&description, "description", "", "calendar description")
	cmd.Flags().StringVar(&email, "email", "", "owner email address (used for RSVP matching)")
	cmd.Flags().StringVar(&remoteURL, "remote-url", "", "remote CalDAV calendar URL")
	cmd.Flags().StringVar(&username, "username", "", "Username for remote authentication")
	cmd.Flags().StringVar(&authType, "auth", "basic", "Auth type: basic, bearer, oauth2")
	cmd.Flags().StringVar(&oauthClientID, "oauth-client-id", "", "OAuth 2.0 client ID")
	cmd.Flags().BoolVar(&allowInsecure, "allow-insecure", false, "Allow HTTP (non-HTTPS) remote URLs")
	return cmd
}

func calendarUpdateCmd() *cobra.Command {
	var (
		name             string
		color            string
		description      string
		email            string
		remoteURL        string
		username         string
		authType         string
		oauthClientID    string
		allowInsecure    bool
		disconnectRemote bool
	)
	cmd := &cobra.Command{
		Use:   "update <id|name>",
		Short: "Update an existing calendar",
		Long: `Update a local calendar's name, color, or description.

Only the flags you pass are changed.`,
		Example: `  chroncal calendar update 1 --name "Deep Work"
  chroncal calendar update Work --color "#2563EB" --description "Focus blocks and deadlines"
  chroncal calendar update Work --remote-url https://cal.example.com/dav/calendars/work/ --username alice --auth bearer`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			cals, err := a.Calendars.List(ctx)
			if err != nil {
				return fmt.Errorf("list calendars: %w", err)
			}
			existing, err := findCalendarByRef(cals, args[0])
			if err != nil {
				return err
			}

			// Preserve the existing remote auth type when re-pointing
			// --remote-url at an already linked calendar without --auth.
			// Defaulting to "basic" here would prompt for a password and
			// overwrite a stored bearer/OAuth token (issue #430).
			if !disconnectRemote && strings.TrimSpace(remoteURL) != "" &&
				!cmd.Flags().Changed("auth") && existing.AccountID != 0 {
				account, err := a.Queries.GetAccount(ctx, existing.AccountID)
				// A missing account row means the link is orphaned; let
				// Connect recreate it from the flag default rather than
				// failing the update. Any other read error is propagated.
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("get account: %w", err)
				}
				if err == nil {
					authType = account.AuthType
				}
			}

			if err := validateCalendarRemoteFlags(remoteURL, username, authType, oauthClientID, allowInsecure, disconnectRemote); err != nil {
				return err
			}

			n, co, d := existing.Name, existing.Color, existing.Description
			if cmd.Flags().Changed("name") {
				n = name
			}
			if cmd.Flags().Changed("color") {
				co = color
			}
			if cmd.Flags().Changed("description") {
				d = description
			}

			c, err := a.Calendars.Update(ctx, existing.ID, n, co, d)
			if err != nil {
				return fmt.Errorf("update calendar: %w", err)
			}

			if cmd.Flags().Changed("email") {
				if err := a.Calendars.SetOwnerEmail(ctx, c.ID, email); err != nil {
					return fmt.Errorf("set owner email: %w", err)
				}
			}

			if disconnectRemote {
				if err := disconnectCalendarRemote(ctx, a, c); err != nil {
					return err
				}
				c, err = a.Calendars.Get(ctx, existing.ID)
				if err != nil {
					return fmt.Errorf("get calendar: %w", err)
				}
			} else if strings.TrimSpace(remoteURL) != "" {
				if err := connectCalendarRemote(ctx, a, c, calendarRemoteFlags{
					RemoteURL:     remoteURL,
					Username:      username,
					AuthType:      authType,
					OAuthClientID: oauthClientID,
					AllowInsecure: allowInsecure,
				}); err != nil {
					return err
				}
				c, err = a.Calendars.Get(ctx, existing.ID)
				if err != nil {
					return fmt.Errorf("get calendar: %w", err)
				}
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONCalendar(c))
			}
			fmt.Fprintf(w, "Updated calendar %d: %s\n", c.ID, c.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&color, "color", "", "new color (hex)")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&email, "email", "", "owner email address (used for RSVP matching)")
	cmd.Flags().StringVar(&remoteURL, "remote-url", "", "remote CalDAV calendar URL")
	cmd.Flags().StringVar(&username, "username", "", "Username for remote authentication")
	cmd.Flags().StringVar(&authType, "auth", "basic", "Auth type: basic, bearer, oauth2")
	cmd.Flags().StringVar(&oauthClientID, "oauth-client-id", "", "OAuth 2.0 client ID")
	cmd.Flags().BoolVar(&allowInsecure, "allow-insecure", false, "Allow HTTP (non-HTTPS) remote URLs")
	cmd.Flags().BoolVar(&disconnectRemote, "disconnect-remote", false, "Remove the remote CalDAV link from this calendar")
	return cmd
}

func calendarDeleteCmd() *cobra.Command {
	var promote string
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a calendar",
		Long: `Delete a local calendar by numeric ID.

Use "chroncal calendar list" first if you need to confirm the ID.

If the target is the current default, pass --promote <id|name> to choose
its replacement. With --yes (no --promote), the alphabetically-first
remaining calendar is promoted automatically and the choice is logged
to stderr.`,
		Example: `  chroncal calendar delete 3
  chroncal calendar delete 3 --output json
  chroncal calendar delete 3 --promote Work`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return errInvalidInputf("invalid calendar ID: %v", err)
			}

			// Load the calendar before prompting so the user sees what's
			// actually at risk (name + event count). Calendar delete
			// cascades through events, todos, and journals, so a generic
			// "Delete calendar 3?" is far too opaque for a one-keystroke
			// destructive confirm.
			cal, getErr := a.Calendars.Get(ctx, id)
			if getErr != nil {
				return notFoundErr(getErr, "calendar", id)
			}
			eventCount, _ := a.Queries.CountEventsByCalendar(ctx, id)

			var (
				newDefaultID   int64
				newDefaultName string
			)
			if cal.IsDefault {
				newDefault, err := resolveNewDefault(cmd, a, ctx, cal, promote)
				if err != nil {
					return err
				}
				newDefaultID = newDefault.ID
				newDefaultName = newDefault.Name
			}

			question := fmt.Sprintf("Delete calendar %q? Its %d event(s) and any todos/journals will be removed.",
				safeText(cal.Name), eventCount)
			if newDefaultID != 0 {
				question += fmt.Sprintf(" %q will become the default.", safeText(newDefaultName))
			}
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if err := deleteCalendarWithCleanup(ctx, a, id, newDefaultID); err != nil {
				return notFoundErr(err, "calendar", id)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				out := map[string]any{"deleted": true, "id": id}
				if newDefaultID != 0 {
					out["promoted_default_id"] = newDefaultID
					out["promoted_default_name"] = newDefaultName
				}
				return printOutput(w, out)
			}
			fmt.Fprintf(w, "Deleted calendar %d.\n", id)
			if newDefaultID != 0 {
				fmt.Fprintf(w, "Promoted %q to default.\n", newDefaultName)
			}
			return nil
		},
	}
	addConfirmFlag(cmd)
	cmd.Flags().StringVar(&promote, "promote", "", "calendar (id or name) to promote to default when deleting the current default")
	return cmd
}

// resolveNewDefault picks the calendar that will take over as default after
// deleting cal. It honors --promote when given, falls back to interactive
// prompt on a TTY, and uses the alphabetically-first remaining calendar
// when running with --yes / CHRONCAL_ASSUME_YES (auditing the choice to
// stderr so scripted callers always see what changed).
func resolveNewDefault(cmd *cobra.Command, a *app.App, ctx context.Context, cal calendarpkg.Calendar, promote string) (calendarpkg.Calendar, error) {
	cals, err := a.Calendars.List(ctx)
	if err != nil {
		return calendarpkg.Calendar{}, fmt.Errorf("list calendars: %w", err)
	}
	candidates := make([]calendarpkg.Calendar, 0, len(cals))
	for _, c := range cals {
		if c.ID != cal.ID {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return calendarpkg.Calendar{}, calendarpkg.ErrLastCalendar
	}

	if promote != "" {
		chosen, err := findCalendarByRef(candidates, promote)
		if err != nil {
			return calendarpkg.Calendar{}, errInvalidInputf("promote: %v", err)
		}
		return chosen, nil
	}

	// Alphabetical preselect — List() already orders by name.
	preselect := candidates[0]

	if assumeYes(cmd) {
		fmt.Fprintf(cmd.ErrOrStderr(), "Promoting %q to default.\n", preselect.Name)
		return preselect, nil
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return calendarpkg.Calendar{}, &cliError{
			Code: "aborted",
			Msg:  "refusing to auto-promote default from a non-interactive shell; pass --promote <id|name>",
		}
	}

	return promptForNewDefault(cmd, cal, candidates, preselect)
}

func promptForNewDefault(cmd *cobra.Command, cal calendarpkg.Calendar, candidates []calendarpkg.Calendar, preselect calendarpkg.Calendar) (calendarpkg.Calendar, error) {
	fmt.Fprintf(cmd.ErrOrStderr(), "%q is the default calendar. Choose its replacement:\n", cal.Name)
	for _, c := range candidates {
		marker := "  "
		if c.ID == preselect.ID {
			marker = "> "
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%s%d  %s\n", marker, c.ID, c.Name)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Enter id or name (default %q): ", preselect.Name)

	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return calendarpkg.Calendar{}, fmt.Errorf("read promotion target: %w", err)
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		return preselect, nil
	}
	chosen, err := findCalendarByRef(candidates, answer)
	if err != nil {
		return calendarpkg.Calendar{}, errInvalidInputf("promote: %v", err)
	}
	return chosen, nil
}

func assumeYes(cmd *cobra.Command) bool {
	if yes, _ := cmd.Flags().GetBool("yes"); yes {
		return true
	}
	return envYes(os.Getenv("CHRONCAL_ASSUME_YES"))
}

func findCalendarByRef(cals []calendarpkg.Calendar, ref string) (calendarpkg.Calendar, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		for _, cal := range cals {
			if cal.ID == id {
				return cal, nil
			}
		}
		return calendarpkg.Calendar{}, fmt.Errorf("calendar %q not found", ref)
	}

	var matches []calendarpkg.Calendar
	for _, cal := range cals {
		if strings.EqualFold(cal.Name, ref) {
			matches = append(matches, cal)
		}
	}
	switch len(matches) {
	case 0:
		return calendarpkg.Calendar{}, fmt.Errorf("calendar %q not found", ref)
	case 1:
		return matches[0], nil
	default:
		return calendarpkg.Calendar{}, fmt.Errorf("calendar name %q is ambiguous; use its numeric ID instead", ref)
	}
}
