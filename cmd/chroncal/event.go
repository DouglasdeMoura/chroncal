package main

import (
	"github.com/spf13/cobra"
)

func eventCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "event",
		Short: "Manage events",
		Long: `Create, search, inspect, update, and delete calendar events.

Events may be one-time or recurring, all-day or timed, and can include
alarms, attendees, attachments, and other iCalendar metadata.`,
		Example: `  chroncal event list
  chroncal event add "Demo" --date 2026-04-10 --time 14:00 --duration 1h
  chroncal event get 42`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		eventListCmd(), eventGetCmd(), eventAddCmd(), eventUpdateCmd(),
		eventRsvpCmd(), eventDeleteCmd(), eventSearchCmd(),
		eventRestoreCmd(), eventPurgeCmd(), eventPurgeDeletedCmd(),
	)
	return cmd
}
