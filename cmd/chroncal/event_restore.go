package main

import (
	"github.com/spf13/cobra"
)

func eventRestoreCmd() *cobra.Command {
	return newRestoreCmd(eventResource, verbHelp{
		short: "Restore a soft-deleted event",
		long: `Restore clears the deletion marker on a soft-deleted event so it
reappears in list and TUI views.

The ID of a series master reverses the series delete: the master and the
overrides that the series delete hid come back. An occurrence deleted on
its own before stays deleted. A UID reverses a series delete of that UID.
An occurrence deleted on its own, or a row that a truncation hid, comes
back by its ID. When two calendars hold a series delete of the UID, pass
the ID of the master.

The event must have been deleted via chroncal (soft-delete, not purged).
Use 'events list --include-deleted' to see deletable candidates.

If the event was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely (with a fresh resource URL).`,
		example: `  chroncal event restore 42
  chroncal event restore my-event-uid`,
	})
}
