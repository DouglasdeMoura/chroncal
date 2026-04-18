package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	calendarpkg "github.com/douglasdemoura/chroncal/internal/calendar"
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
	}
	cmd.AddCommand(
		calendarListCmd(),
		calendarGetCmd(),
		calendarCreateCmd(),
		calendarUpdateCmd(),
		calendarDeleteCmd(),
	)
	return cmd
}

func calendarListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all calendars",
		Long:  `Show the local calendars available in your chroncal database.`,
		Example: `  chroncal calendar list
  chroncal calendar list --output json`,
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
			printCalendars(w, cals)
			return nil
		},
	}
	return cmd
}

func calendarGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get calendar details by ID",
		Long:  `Show one calendar, including its metadata and sync link details.`,
		Example: `  chroncal calendar get 1
  chroncal calendar get 1 --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid calendar ID: %w", err)
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
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("calendar name must not be empty")
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
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			if err := validateCalendarRemoteFlags(remoteURL, username, authType, oauthClientID, allowInsecure, disconnectRemote); err != nil {
				return err
			}

			cals, err := a.Calendars.List(ctx)
			if err != nil {
				return fmt.Errorf("list calendars: %w", err)
			}
			existing, err := findCalendarByRef(cals, args[0])
			if err != nil {
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
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a calendar",
		Long: `Delete a local calendar by numeric ID.

Use "chroncal calendar list" first if you need to confirm the ID.`,
		Example: `  chroncal calendar delete 3
  chroncal calendar delete 3 --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid calendar ID: %w", err)
			}

			if err := deleteCalendarWithCleanup(ctx, a, id); err != nil {
				return notFoundErr(err, "calendar", id)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"deleted": true, "id": id})
			}
			fmt.Fprintf(w, "Deleted calendar %d.\n", id)
			return nil
		},
	}
	return cmd
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

	for _, cal := range cals {
		if strings.EqualFold(cal.Name, ref) {
			return cal, nil
		}
	}
	return calendarpkg.Calendar{}, fmt.Errorf("calendar %q not found", ref)
}
