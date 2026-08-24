package main

import (
	"github.com/spf13/cobra"
)

func eventRestoreCmd() *cobra.Command {
	return newRestoreCmd(eventResource, verbHelp{
		short: "Restore a soft-deleted event",
		long: `Restore clears the deletion marker on a soft-deleted event so it
reappears in list and TUI views.

The event must have been deleted via chroncal (soft-delete, not purged).
Use 'events list --include-deleted' to see deletable candidates.

If the event was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely (with a fresh resource URL).`,
		example: `  chroncal event restore 42
  chroncal event restore my-event-uid`,
	})
}
