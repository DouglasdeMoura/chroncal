package main

import (
	"github.com/spf13/cobra"
)

func eventRestoreCmd() *cobra.Command {
	return newRestoreCmd(eventResource, verbHelp{
		short: "Restore a soft-deleted event",
		long: `Restore clears the deletion marker on a soft-deleted event so it
reappears in list and TUI views.

The ID of a series master restores the series in its calendar: the master,
its overrides in the trash, and the occurrences their deletes excluded. A
UID restores the one series in the trash with that UID. When two calendars
hold the UID in the trash, pass the ID of the master.

The event must have been deleted via chroncal (soft-delete, not purged).
Use 'events list --include-deleted' to see deletable candidates.

If the event was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely (with a fresh resource URL).`,
		example: `  chroncal event restore 42
  chroncal event restore my-event-uid`,
	})
}
